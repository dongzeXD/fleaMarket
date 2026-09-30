package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
)

// 索引名与用于重建的临时索引名。
//
// 对外始终使用 indexName；重建时在 rebuildIndexName 上构建后交换，
// 使搜索在重建期间不中断。
const (
	indexName        = "products"
	rebuildIndexName = "products_rebuild"
)

// MeiliConfig Meilisearch 连接配置。
type MeiliConfig struct {
	Host string
	// APIKey 主密钥。生产必须设置，否则任何人都能改写索引。
	APIKey   string
	Timeouts Timeouts
}

// Meili 基于 Meilisearch 的 Engine 实现。
type Meili struct {
	client meilisearch.ServiceManager
	cfg    MeiliConfig
	log    *slog.Logger
}

var _ Engine = (*Meili)(nil)

// NewMeili 创建引擎。
func NewMeili(c MeiliConfig, logger *slog.Logger) (*Meili, func(), error) {
	if c.Host == "" {
		return nil, nil, errors.New("engine: empty meilisearch host")
	}
	c.Timeouts.setDefaults()

	client := meilisearch.New(c.Host, meilisearch.WithAPIKey(c.APIKey))
	m := &Meili{client: client, cfg: c, log: logger.With(slog.String("engine", "meilisearch"))}
	return m, func() { client.Close() }, nil
}

// EnsureIndex 幂等创建索引并写入字段配置。
//
// 字段配置是代码的一部分：Meilisearch 只允许对声明过的字段做筛选与排序，
// 漏配的后果是「筛选条件被静默忽略」——返回结果看似正常但没应用过滤，
// 这类问题在测试中极难发现。
func (m *Meili) EnsureIndex(ctx context.Context) error {
	if _, err := m.client.GetIndexWithContext(ctx, indexName); err != nil {
		task, err := m.client.CreateIndexWithContext(ctx, &meilisearch.IndexConfig{
			Uid:        indexName,
			PrimaryKey: "id",
		})
		if err != nil {
			return m.wrap(err)
		}
		if err := m.waitTask(ctx, task.TaskUID); err != nil {
			return err
		}
	}
	return m.applySettings(ctx, indexName)
}

func (m *Meili) applySettings(ctx context.Context, uid string) error {
	idx := m.client.Index(uid)

	settings := &meilisearch.Settings{
		// 可筛选字段。缺一个就会让对应的筛选条件被静默忽略。
		FilterableAttributes: []string{
			"status", "category_id", "parent_category_id",
			"campus_id", "school_id", "condition", "price_cents", "seller_id",
		},
		// 可排序字段。
		SortableAttributes: []string{"created_at", "price_cents", "heat_score"},
		// 可搜索字段及其优先级：标题的权重高于描述，
		// 因为标题是卖家对商品最凝练的表述。
		SearchableAttributes: []string{"title", "description"},
		// 排序规则。words/typo 在前保证相关度优先，
		// sort 在 typo 之后使显式排序能覆盖默认相关度排序。
		RankingRules: []string{
			"words", "typo", "proximity", "attribute", "sort", "exactness",
		},
		// 返回字段白名单：不返回 description 可显著减小响应体，
		// 列表页不需要它。
		DisplayedAttributes: []string{
			"id", "seller_id", "title", "price_cents", "condition", "status",
			"category_id", "parent_category_id", "school_id", "campus_id",
			"cover_key", "heat_score", "favorite_count", "created_at",
		},
	}

	task, err := idx.UpdateSettingsWithContext(ctx, settings)
	if err != nil {
		return m.wrap(err)
	}
	return m.waitTask(ctx, task.TaskUID)
}

func (m *Meili) Upsert(ctx context.Context, docs ...Document) error {
	if len(docs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Index)
	defer cancel()

	if _, err := m.client.Index(indexName).AddDocumentsWithContext(ctx, docs, docOpts()); err != nil {
		return m.wrap(err)
	}
	// 不等待任务完成：索引是异步的，等待会让 MQ 消费速度受限于索引速度。
	// 最终一致性在此场景可接受——秒级延迟不影响体验。
	return nil
}

func (m *Meili) Delete(ctx context.Context, productIDs ...int64) error {
	if len(productIDs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Index)
	defer cancel()

	ids := make([]string, 0, len(productIDs))
	for _, id := range productIDs {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	if _, err := m.client.Index(indexName).DeleteDocumentsWithContext(ctx, ids, nil); err != nil {
		return m.wrap(err)
	}
	return nil
}

func (m *Meili) Search(ctx context.Context, q Query) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Search)
	defer cancel()

	req := &meilisearch.SearchRequest{
		Offset: int64(q.Offset),
		Limit:  int64(q.Limit),
	}
	if f := buildFilter(q.Filter); f != "" {
		req.Filter = f
	}
	if s := buildSort(q.Sort); len(s) > 0 {
		req.Sort = s
	}

	res, err := m.client.Index(indexName).SearchWithContext(ctx, q.Keyword, req)
	if err != nil {
		return nil, m.wrap(err)
	}

	docs := make([]Document, 0, len(res.Hits))
	for _, hit := range res.Hits {
		var d Document
		raw, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			// 单条文档解析失败不影响整页结果：
			// 索引里可能残留旧结构的文档，跳过比整页报错更合理。
			m.log.WarnContext(ctx, "索引文档解析失败", slog.Any("error", err))
			continue
		}
		docs = append(docs, d)
	}
	return &Result{Documents: docs, EstimatedTotal: res.EstimatedTotalHits}, nil
}

func (m *Meili) Facets(ctx context.Context, keyword string, f Filter) (*FacetResult, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Search)
	defer cancel()

	req := &meilisearch.SearchRequest{
		Limit:  0, // 只要分面统计，不要文档
		Facets: []string{"category_id", "condition", "campus_id"},
	}
	if s := buildFilter(f); s != "" {
		req.Filter = s
	}

	res, err := m.client.Index(indexName).SearchWithContext(ctx, keyword, req)
	if err != nil {
		return nil, m.wrap(err)
	}

	out := &FacetResult{
		Categories: map[string]int64{},
		Conditions: map[string]int64{},
		Campuses:   map[string]int64{},
	}
	// FacetDistribution 是 json.RawMessage：各维度的取值是动态的，
	// 客户端库无法为它生成静态类型，需自行解码。
	if len(res.FacetDistribution) > 0 {
		var dist map[string]map[string]int64
		if err := json.Unmarshal(res.FacetDistribution, &dist); err != nil {
			return nil, fmt.Errorf("engine: decode facet distribution: %w", err)
		}
		for field, bucket := range dist {
			target := out.pick(field)
			if target == nil {
				continue
			}
			for k, n := range bucket {
				target[k] = n
			}
		}
	}
	return out, nil
}

func (r *FacetResult) pick(field string) map[string]int64 {
	switch field {
	case "category_id":
		return r.Categories
	case "condition":
		return r.Conditions
	case "campus_id":
		return r.Campuses
	default:
		return nil
	}
}

// docOpts 指定主键。
//
// 显式传而非依赖推断：Meilisearch 会用第一个名为 *id 的字段当主键，
// 我们的文档里有 seller_id / category_id 等多个 id 后缀字段，
// 靠推断存在选错的风险。
func docOpts() *meilisearch.DocumentOptions {
	pk := "id"
	return &meilisearch.DocumentOptions{PrimaryKey: &pk}
}

func (m *Meili) Stats(ctx context.Context) (*Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Health)
	defer cancel()

	st, err := m.client.Index(indexName).GetStatsWithContext(ctx, nil)
	if err != nil {
		return nil, m.wrap(err)
	}
	return &Stats{DocumentCount: st.NumberOfDocuments, Indexing: st.IsIndexing}, nil
}

func (m *Meili) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeouts.Health)
	defer cancel()

	if !m.client.IsHealthy() {
		return ErrUnavailable
	}
	if _, err := m.client.GetIndexWithContext(ctx, indexName); err != nil {
		return m.wrap(err)
	}
	return nil
}

// SwapForRebuild 在临时索引上全量重建后交换。
//
// 步骤：删除残留的临时索引 → 创建并配置 → 分批写入 → 等待索引完成 → 交换别名。
// 交换是 Meilisearch 的原子操作，用户不会看到中间态。
func (m *Meili) SwapForRebuild(ctx context.Context,
	feed func(yield func(Document) error) error) (int64, error) {

	// 清理上次失败留下的临时索引，否则会带着脏数据继续写。
	if task, err := m.client.DeleteIndexWithContext(ctx, rebuildIndexName); err == nil {
		_ = m.waitTask(ctx, task.TaskUID)
	}

	task, err := m.client.CreateIndexWithContext(ctx, &meilisearch.IndexConfig{
		Uid:        rebuildIndexName,
		PrimaryKey: "id",
	})
	if err != nil {
		return 0, m.wrap(err)
	}
	if err := m.waitTask(ctx, task.TaskUID); err != nil {
		return 0, err
	}
	if err := m.applySettings(ctx, rebuildIndexName); err != nil {
		return 0, err
	}

	const batchSize = 500
	batch := make([]Document, 0, batchSize)
	var total int64

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		t, err := m.client.Index(rebuildIndexName).
			AddDocumentsWithContext(ctx, batch, docOpts())
		if err != nil {
			return m.wrap(err)
		}
		// 重建路径上等待每批完成：这里不在用户请求链路上，
		// 等待可以尽早发现写入失败，避免交换一个不完整的索引。
		if err := m.waitTask(ctx, t.TaskUID); err != nil {
			return err
		}
		total += int64(len(batch))
		batch = batch[:0]
		return nil
	}

	err = feed(func(d Document) error {
		batch = append(batch, d)
		if len(batch) >= batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return total, err
	}
	if err := flush(); err != nil {
		return total, err
	}

	swapTask, err := m.client.SwapIndexesWithContext(ctx, []*meilisearch.SwapIndexesParams{
		{Indexes: []string{indexName, rebuildIndexName}},
	})
	if err != nil {
		return total, m.wrap(err)
	}
	if err := m.waitTask(ctx, swapTask.TaskUID); err != nil {
		return total, err
	}

	// 交换后 rebuildIndexName 指向的是旧索引，删掉释放空间。
	if t, err := m.client.DeleteIndexWithContext(ctx, rebuildIndexName); err == nil {
		_ = m.waitTask(ctx, t.TaskUID)
	}
	return total, nil
}

// waitTask 等待异步任务完成。
func (m *Meili) waitTask(ctx context.Context, taskUID int64) error {
	task, err := m.client.WaitForTaskWithContext(ctx, taskUID, 100*time.Millisecond)
	if err != nil {
		return m.wrap(err)
	}
	if task.Status != meilisearch.TaskStatusSucceeded {
		return fmt.Errorf("engine: task %d %s: %v", taskUID, task.Status, task.Error)
	}
	return nil
}

// wrap 把连接类错误归一为 ErrUnavailable，使调用方能据此降级。
//
// 只有「引擎不可达」才算不可用；语法错误之类的 4xx 是调用方的 bug，
// 归入不可用会掩盖问题并让降级逻辑被错误触发。
func (m *Meili) wrap(err error) error {
	if err == nil {
		return nil
	}
	var meiliErr *meilisearch.Error
	if errors.As(err, &meiliErr) {
		if meiliErr.StatusCode >= 500 || meiliErr.StatusCode == 0 {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return err
	}
	// 网络错误、超时、连接被拒都归为不可用。
	if errors.Is(err, context.DeadlineExceeded) || isConnError(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}

func isConnError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "timeout")
}

// buildFilter 把 Filter 转成 Meilisearch 的过滤表达式。
func buildFilter(f Filter) string {
	var parts []string

	if len(f.Statuses) > 0 {
		parts = append(parts, inExpr("status", uint8sToStrings(f.Statuses)))
	}
	if len(f.Conditions) > 0 {
		parts = append(parts, inExpr("condition", uint8sToStrings(f.Conditions)))
	}
	if len(f.CategoryIDs) > 0 {
		// 同时匹配二级与一级分类：用户点「数码电子」时应看到其下全部商品，
		// 而 parent_category_id 已冗余在文档中，无需先查子分类列表。
		ids := int64sToStrings(f.CategoryIDs)
		parts = append(parts, "("+inExpr("category_id", ids)+" OR "+
			inExpr("parent_category_id", ids)+")")
	}
	if len(f.CampusIDs) > 0 {
		parts = append(parts, inExpr("campus_id", int64sToStrings(f.CampusIDs)))
	}
	if len(f.SchoolIDs) > 0 {
		parts = append(parts, inExpr("school_id", int64sToStrings(f.SchoolIDs)))
	}
	if f.MinPriceCents > 0 {
		parts = append(parts, "price_cents >= "+strconv.FormatInt(f.MinPriceCents, 10))
	}
	if f.MaxPriceCents > 0 {
		parts = append(parts, "price_cents <= "+strconv.FormatInt(f.MaxPriceCents, 10))
	}
	if f.ExcludeSellerID > 0 {
		parts = append(parts, "seller_id != "+strconv.FormatInt(f.ExcludeSellerID, 10))
	}
	if f.ExcludeCampusID > 0 {
		parts = append(parts, "campus_id != "+strconv.FormatInt(f.ExcludeCampusID, 10))
	}
	if f.ExcludeSchoolID > 0 {
		parts = append(parts, "school_id != "+strconv.FormatInt(f.ExcludeSchoolID, 10))
	}
	return strings.Join(parts, " AND ")
}

func inExpr(field string, values []string) string {
	if len(values) == 1 {
		return field + " = " + values[0]
	}
	return field + " IN [" + strings.Join(values, ", ") + "]"
}

func uint8sToStrings(vs []uint8) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, strconv.FormatUint(uint64(v), 10))
	}
	return out
}

func int64sToStrings(vs []int64) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, strconv.FormatInt(v, 10))
	}
	return out
}

// buildSort 转换排序方式。
//
// SortHot 用热度分倒序，SortLatest 用创建时间倒序。
// 返回空切片表示用引擎的默认相关度排序——有关键词时这通常是最优选择。
func buildSort(s Sort) []string {
	switch s {
	case SortLatest:
		return []string{"created_at:desc"}
	case SortHot:
		return []string{"heat_score:desc"}
	case SortPriceAsc:
		return []string{"price_cents:asc"}
	case SortPriceDesc:
		return []string{"price_cents:desc"}
	default:
		return nil
	}
}
