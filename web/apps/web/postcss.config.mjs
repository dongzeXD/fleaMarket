// Tailwind v4 通过 PostCSS 插件接入，无需 tailwind.config.js——
// 主题与断点直接写在 CSS 的 @theme 块里。
export default {
  plugins: {
    '@tailwindcss/postcss': {},
  },
};
