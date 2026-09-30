-- 开发环境演示数据：一所学校、两个校区、若干面交点。
--
-- 仅用于本地开发与联调，生产环境不应执行本文件
-- （make seed-dev 会检查 APP_ENV）。
--
-- ID 约定同分类表：字典数据用固定小整数，跨环境稳定。

-- ---------- 学校与校区（user 分组）----------
INSERT INTO t_school (id, name, province, email_domains, enabled, created_at, updated_at) VALUES
  (1, '示例大学', '北京', 'stu.example.edu.cn,example.edu.cn', 1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
  name = VALUES(name), province = VALUES(province),
  email_domains = VALUES(email_domains), enabled = VALUES(enabled);

INSERT INTO t_campus (id, school_id, name, lat, lng, enabled, created_at, updated_at) VALUES
  (11, 1, '主校区', 39.9908500, 116.3055800, 1, NOW(), NOW()),
  (12, 1, '东校区', 39.9835200, 116.3162400, 1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
  name = VALUES(name), lat = VALUES(lat), lng = VALUES(lng), enabled = VALUES(enabled);

-- ---------- 面交地点（product 分组）----------
--
-- recommended=1 的是人流量大、有监控的公共区域。
-- 面交安全的核心措施是「在公开场所交易」，因此把这类地点标出来并优先推荐，
-- 比事后处理纠纷有效得多。
INSERT INTO t_campus_poi (id, campus_id, name, type, lat, lng, recommended, enabled, created_at, updated_at) VALUES
  (1101, 11, '东门',        1, 39.9910000, 116.3080000, 1, 1, NOW(), NOW()),
  (1102, 11, '南门',        1, 39.9890000, 116.3055000, 1, 1, NOW(), NOW()),
  (1103, 11, '图书馆门口',   4, 39.9905000, 116.3060000, 1, 1, NOW(), NOW()),
  (1104, 11, '第一食堂',     3, 39.9912000, 116.3045000, 1, 1, NOW(), NOW()),
  (1105, 11, '第二食堂',     3, 39.9898000, 116.3070000, 1, 1, NOW(), NOW()),
  (1106, 11, '快递驿站',     6, 39.9915000, 116.3050000, 1, 1, NOW(), NOW()),
  (1107, 11, '一号宿舍楼下', 2, 39.9920000, 116.3040000, 0, 1, NOW(), NOW()),
  (1108, 11, '三号教学楼',   5, 39.9900000, 116.3035000, 0, 1, NOW(), NOW()),
  (1201, 12, '东校区正门',   1, 39.9840000, 116.3160000, 1, 1, NOW(), NOW()),
  (1202, 12, '东校区食堂',   3, 39.9832000, 116.3165000, 1, 1, NOW(), NOW()),
  (1203, 12, '东校区快递点', 6, 39.9838000, 116.3170000, 1, 1, NOW(), NOW())
ON DUPLICATE KEY UPDATE
  name = VALUES(name), type = VALUES(type), lat = VALUES(lat), lng = VALUES(lng),
  recommended = VALUES(recommended), enabled = VALUES(enabled);
