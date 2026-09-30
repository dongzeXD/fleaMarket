import type { NextConfig } from 'next';

const config: NextConfig = {
  reactStrictMode: true,
  // 允许从对象存储/CDN 加载商品图。
  // 必须显式列出域名——next/image 默认拒绝任意远端源，
  // 否则站点会成为他人图片的免费代理与流量放大器。
  images: {
    remotePatterns: [
      { protocol: 'http', hostname: '127.0.0.1', port: '9000', pathname: '/**' },
      { protocol: 'http', hostname: 'localhost', port: '9000', pathname: '/**' },
    ],
  },
  // 网关地址通过环境变量注入，构建产物不绑定具体环境。
  env: {
    NEXT_PUBLIC_API_BASE_URL: process.env.NEXT_PUBLIC_API_BASE_URL ?? 'http://127.0.0.1:8000',
  },
};

export default config;
