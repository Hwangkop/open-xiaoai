# Open XiaoAI 管理台（前端）

独立部署的管理界面，通过 HTTP API 与 `apps/chat` 通信。

## 技术栈

- **React 19 + TypeScript**
- **Vite**：开发服务器与构建
- **Tailwind CSS v4**（`@tailwindcss/vite`）
- **lucide-react**：图标
- **radix-ui + class-variance-authority**：无样式交互组件（对话框、分隔线等）

`src/components/ui/` 下的 `button` / `input` / `textarea` / `card` / `badge` / `separator` / `dialog` 以及 `src/styles.css` 里的 `@theme` / `.dark` 变量块，取自 ZCode 的 `packages/ui`（同一个作者的另一套 UI 体系），按原样复制，仅调整了导入路径、去掉 i18n 依赖。

## 开发

```shell
pnpm install
pnpm dev        # http://127.0.0.1:5173
```

开发时在页面「连接设置」里把 API 地址指向本机 chat（如 `http://127.0.0.1:4399`）。后端已对 `/admin/api/*` 开启 CORS。

## 构建

```shell
pnpm build      # 产物在 dist/
```

`dist/` 是纯静态站点，可放在任意静态服务器或子路径下（`vite.config.ts` 里 `base: './'`）。

## 目录

```
src/
  App.tsx              侧边栏布局、路由切换、Toast
  views/
    DoubaoView.tsx     登录状态、扫码、会话列表与消息
    ConfigView.tsx     YAML 配置编辑（Ctrl/⌘+S 保存）
    TtsView.tsx        发送文本到音箱试听
  components/
    ConnectionDialog.tsx
    ui/                复制的 UI 基础组件
  lib/
    api.ts             HTTP 封装、本地设置存储
    useToasts.ts       Toast 状态
```

## 说明

- 页面不依赖任何 CDN，构建产物可直接离线部署。
- 连接地址与账号密码保存在浏览器 `localStorage`，不会发送到其他服务。
