# Coral Anchor 视觉设计体系与跨项目复刻指南

> 本文是给设计师和 AI 编程助手使用的**视觉重构规范**：在不改变目标项目业务能力、数据语义和交互逻辑的前提下，让其界面在视觉上尽可能接近 GPT-Load 当前现代版。基于本仓库 **2026-10-28 当前工作区源码**抽取，包含当时未提交的修改，并非某个已发布版本的快照。
>
> 本文只规范现代版（`web/src/frontends/modern/`），不混入仍可选用的 classic 经典版。`screenshot/groups-zh-CN.png` 和 `screenshot/usage-zh-CN.png` 是历史视觉参考，可能落后于当前源码；冲突时以当前现代版源码和本文标明的组件实现为准。

---

## 1. 给 AI 的执行指令

如果你正在把另一个项目重构为本设计风格，请按以下顺序执行：

1. **先阅读目标项目**：识别技术栈、页面、现有组件、主题、路由、权限、数据和交互状态。不要先改业务代码。
2. **先建立全局设计 token，再改公共组件，最后改页面**。优先复用目标项目现有框架与组件库，不因为来源项目使用 Vue 就强制迁移技术栈。
3. 视觉结构、颜色、字号、间距、控件状态、响应式和暗色都依照本文；目标项目没有的业务页面不必照搬，有额外页面则套用最接近的页面模式。
4. 保留现有 API、字段、路由、权限、功能和语义。不要把目标项目硬改为 GPT-Load，不要用伪造数据代替真实业务，不要删除功能来追求截图相似。
5. 品牌形状和第三方商标**不默认迁移**：原项目 Logo、吉祥物、名称及渠道图标属于其身份或第三方资产；迁移设计语言即可。仅在已取得使用授权、确实需要原品牌时复用原文件和许可说明。
6. 明确实现亮色、暗色、跟随系统、键盘焦点、加载、空、错误、禁用、选中、移动端以及 reduced motion。不可只完成桌面亮色静态截图。
7. 依照第 12 节进行视口、数据和状态固定后的视觉验收；无法实测时明确说明未验证，不宣称像素级一致。

可直接使用的任务提示词见第 13 节。

## 2. 设计方向：克制、紧凑、清晰的管理工作区

### 一句话

**暖中性灰工作区 + 亮珊瑚橙操作强调 + 精细边框 + 高密度信息布局。**页面要显得轻、稳、专业，而不是营销落地页、玻璃拟态或重阴影 SaaS 模板。

### 视觉原则

- **大面积中性色，小面积橙红色**：主内容白色，侧栏暖灰；暗色使用中性深灰，而不是纯黑或蓝黑。强调色用于主要操作、当前导航、选中和焦点，不整页铺橙。
- **边框优先，阴影克制**：普通 panel 为 1px 细边框加小圆角，通常不加阴影；仅控件、菜单、弹窗、登录卡使用相应层次的轻阴影。
- **宽工作区、高信息密度**：桌面固定侧栏和顶栏，内容随可用宽度扩展，没有统一的居中最大宽度。表格、筛选和图表排列清晰但紧凑。
- **结构表达层级**：用布局、分组、字体大小、细分隔线和弱背景区分层级，避免满屏大卡片、强对比彩色标题和装饰图案。
- **语义色各司其职**：成功绿、警告黄、危险红、信息蓝用于状态；图表系列、协议或品牌身份色独立定义，不与状态含义混用。
- **手机重新组织，而非等比缩小**：桌面侧栏改底部导航，宽表改摘要行，复杂筛选与编辑迁移到 sheet／全屏面板。
- **原生、轻快、可访问**：系统字体、Lucide 线性图标、短过渡和可见焦点；优先信息可读性，不依赖动画传达状态。

### 不要做

不要使用大面积橙色背景、巨型留白英雄区、深蓝灰底、紫色渐变、彩虹色图标、玻璃模糊、重投影、20px 以上普遍大圆角、浮夸弹簧动画，或将每个数据字段装进一张独立卡片。普通面板不应普遍加阴影。

## 3. 设计 Token：可直接移植的 CSS 基线

以下以中性 `--ds-*` 命名，值对应源项目 `--modern-*`。建议直接复制，再让所有公共组件通过语义 token 使用颜色，不要在页面中随手硬编码。CSS 使用 `light-dark()` 和 `color-mix()`；目标浏览器若不支持，需以主题类或 `data-theme` 提供等价亮暗值回退。

```css
:root {
  color-scheme: light dark;

  /* 基础表面和文字 */
  --ds-canvas: light-dark(#ffffff, #202020);
  --ds-sidebar: light-dark(#f0f0ed, #181818);
  --ds-surface: light-dark(#ffffff, #272727);
  --ds-text: light-dark(#1c1c1b, #f5f5f2);
  --ds-muted: light-dark(#6c6c67, #b5b5b0);
  --ds-border: light-dark(#e4e4df, #40403c);
  --ds-accent: light-dark(#e84518, #ff845e);
  --ds-accent-soft: light-dark(#fff3ed, #3e2a23);
  --ds-subtle: color-mix(in srgb, var(--ds-accent-soft) 15%, var(--ds-surface));
  --ds-action: #ff4f1f;
  --ds-action-hover: #f04012;
  --ds-on-action: #ffffff;

  /* 语义状态，文字和浅底成对使用 */
  --ds-danger: light-dark(#dc3737, #ff8a80);
  --ds-danger-soft: light-dark(#fff1f0, #3e2624);
  --ds-success: light-dark(#1a9b55, #68d99a);
  --ds-success-soft: light-dark(#edf9f1, #21362a);
  --ds-warning: light-dark(#b87408, #f4bc52);
  --ds-warning-soft: light-dark(#fff8e8, #3b321f);
  --ds-info: light-dark(#377de2, #89b9ff);
  --ds-info-soft: light-dark(#edf5ff, #233247);

  /* 控件 */
  --ds-control-border: light-dark(#dcdcd6, #4a4a45);
  --ds-control-border-hover: light-dark(#bdbdb5, #707069);
  --ds-control-hover: color-mix(in srgb, var(--ds-accent-soft) 40%, var(--ds-surface));
  --ds-control-pressed: color-mix(in srgb, var(--ds-accent-soft) 80%, var(--ds-surface));
  --ds-control-disabled: light-dark(#f4f4f1, #2b2b29);
  --ds-placeholder: light-dark(#85857d, #999991);
  --ds-switch-off: light-dark(#ccccc4, #62625b);
  --ds-overlay: rgb(20 20 18 / 35%);
  --ds-loading-overlay: color-mix(in srgb, var(--ds-surface) 78%, transparent);

  /* 图表系列固定语义；不要拿来表示成功/失败 */
  --ds-chart-input: light-dark(#527eb2, #96b9e4);
  --ds-chart-cache: light-dark(#318d91, #82ced0);
  --ds-chart-write: light-dark(#b38b46, #dfbd82);
  --ds-chart-output: light-dark(#8b72b0, #bea7de);
  --ds-chart-cost: light-dark(#318d91, #82ced0);
  --ds-chart-fill: color-mix(in srgb, currentColor 10%, transparent);
  --ds-chart-fill-soft: color-mix(in srgb, currentColor 8%, transparent);

  --ds-font-sans: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC',
    'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
  --ds-font-mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  --ds-text-caption: 11px;
  --ds-text-small: 12px;
  --ds-text-secondary: 13px;
  --ds-text-body: 14px;
  --ds-text-section: 15px;
  --ds-text-mobile-input: 16px;
  --ds-text-title: 24px;
  --ds-weight-regular: 400;
  --ds-weight-medium: 500;
  --ds-weight-semibold: 600;
  --ds-leading-body: 1.6;
  --ds-leading-compact: 1.4;
  --ds-leading-title: 1.35;
  --ds-tracking-title: -0.035em;
  --ds-tracking-label: 0.04em;

  --ds-space-0-5: 2px;
  --ds-space-1: 4px;
  --ds-space-1-5: 6px;
  --ds-space-2: 8px;
  --ds-space-3: 12px;
  --ds-space-4: 16px;
  --ds-space-5: 20px;
  --ds-space-6: 24px;
  --ds-space-8: 32px;
  --ds-space-10: 40px;
  --ds-space-12: 48px;
  --ds-line: 1px;
  --ds-radius-small: 6px;
  --ds-radius-control: 8px;
  --ds-radius-panel: 10px;
  --ds-radius-dialog: 12px;
  --ds-radius-round: 50%;
  --ds-shadow-tooltip: 0 2px 8px rgb(0 0 0 / 6%);
  --ds-shadow-menu: 0 8px 28px rgb(0 0 0 / 12%);
  --ds-shadow-dialog: 0 20px 75px rgb(0 0 0 / 20%);
  --ds-shadow-control: 0 1px 2px rgb(0 0 0 / 4%);
  --ds-shadow-thumb: 0 1px 3px rgb(0 0 0 / 18%);
  --ds-shadow-focus: 0 0 0 3px color-mix(in srgb, var(--ds-accent) 15%, transparent);
  --ds-shadow-focus-danger: 0 0 0 3px color-mix(in srgb, var(--ds-danger) 15%, transparent);

  --ds-control-xxs: 24px;
  --ds-control-xs: 28px;
  --ds-control-sm: 32px;
  --ds-control-md: 36px;
  --ds-control-nav: 40px;
  --ds-touch-target: 44px;
  --ds-icon-xs: 12px;
  --ds-icon-sm: 14px;
  --ds-icon-md: 18px;
  --ds-icon-lg: 20px;
  --ds-icon-stroke: 1.8;
  --ds-sidebar-expanded: 232px;
  --ds-sidebar-collapsed: 72px;
  --ds-topbar-height: 56px;
  --ds-content-inset: 32px;
  --ds-content-bottom: 36px;
  --ds-panel-inset: 24px;
  --ds-page-gap: 24px;
  --ds-motion-fast: 140ms;
  --ds-motion-ease: ease;
  --ds-disabled-opacity: 0.55;
}
:root[data-theme='light'] { color-scheme: light; }
:root[data-theme='dark'] { color-scheme: dark; }
@media (min-width: 1700px) { :root { --ds-content-inset: 40px; } }
@media (max-width: 1150px) { :root { --ds-content-inset: 24px; } }
@media (max-width: 760px) {
  :root { --ds-content-inset: 16px; --ds-content-bottom: 28px; --ds-panel-inset: 16px; }
}
*, *::before, *::after { box-sizing: border-box; }
body {
  min-width: 320px;
  margin: 0;
  color: var(--ds-text);
  background: var(--ds-canvas);
  font: 400 var(--ds-text-body) / var(--ds-leading-body) var(--ds-font-sans);
  -webkit-font-smoothing: antialiased;
}
:focus-visible { outline: 2px solid var(--ds-accent); outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { animation: none !important; transition: none !important; }
}
```

**重要区别**：`accent` 是随亮暗主题变化的文字、边框与选中强调色；实心主要按钮底色是固定 `action: #ff4f1f`，白色按钮文字不随主题变化。不要把二者合并。

### 补充 token

- 状态条高 8px；状态绿取 success，警告 `#eeae36 / #eab84f`，危险取 danger，中性 `#b9b9b1 / #6d6d66`（亮 / 暗）。
- 滚动条 thumb `#cacac3 / #565650`、hover `#a5a59c / #797970`；支持的精细指针环境中为 4px，保留系统可访问性和强制颜色模式。
- Tooltip 背景为 accent-soft 25% 混合 surface，边线为 accent 18% 混合 border；文字使用 muted。
- 品牌 badge 文字为 accent 80% 混合 muted，背景为 accent-soft 55% 混合 surface。
- 禁用透明度通常 0.55；较弱提示透明度 0.65。
- `color-mix` 的比例是设计的一部分，不应粗暴替换为在亮色下取样的一种固定颜色，否则暗色会失真。

## 4. 字体、图标与内容表达

- 正文系统字体 14px / 1.6 / 400；控件和次级正文通常 13px；辅助说明 12px，极小标签 11px。小字只用于次要信息，不用于长篇正文。
- 桌面页面标题 24px / 600 / 1.35，字距 `-0.035em`；移动顶栏标题 15px。区块标题 15px / 600，说明 12px muted。
- 代码、API 地址、模型名、密钥及适合对齐的技术值使用 mono；普通叙述和按钮使用 sans。保留系统字体栈，不额外引入远程字体。
- 图标选 Lucide 风格：线性、统一 1.8 描边，默认 18px，小图标 12/14px，大图标 20px。图标和文字间通常 6–12px；不使用 emoji 代替功能图标。
- 只有图标的按钮必须提供可访问名称和 tooltip；状态不能只靠颜色表达，应配文字、形状或图标。
- 页面标题、统计值和标签使用清晰的层级，避免所有内容都加粗。长标题/地址适当省略，完整值可通过详情或提示查看。

## 5. 全局布局和导航

### 桌面壳层

```text
┌──────────────── 232px ────────────────┬────────────────── 宽工作区 ──────────────────┐
│ 品牌区（与顶栏等高）                    │ 页面标题              刷新 / 主题 / 语言 / 操作 │ 56px
├───────────────────────────────────────┼─────────────────────────────────────────────┤
│ 工作区                                 │ 页面工具栏、筛选、内容区                      │
│   导航项                               │                                             │
│ 运行观测                               │ 列表 / 卡片 / 图表，自适应可用宽度             │
│   导航项                               │                                             │
│ 系统                                   │                                             │
│ 底部外链 / 版本                        │                                             │
└───────────────────────────────────────┴─────────────────────────────────────────────┘
```

- 外壳按 `100dvh` 占满视口，采用 `232px minmax(0, 1fr)` 网格；收起侧栏为 72px。侧栏与内容可各自滚动，主区为 56px 顶栏加内容滚动区。需确保 `min-width: 0` / `min-height: 0`，防止嵌套滚动溢出。
- 侧栏底色 `sidebar`，右边 1px 分隔；主内容 canvas，顶栏下边 1px 分隔。默认内容左右留白 32px；宽屏 40px，紧凑桌面 24px，移动 16px。页面区块间距 24px；不设全局内容最大宽度。
- 侧栏品牌与顶栏同高；导航按业务分组。导航行至少 40px，高亮项用 accent-soft 底、accent 字、600 字重，并有左侧约 3px 短竖线。普通项 muted，hover 用 surface 和主文字。
- 收起侧栏只保留居中图标，提供 tooltip；折叠控件位于侧栏与内容分界线垂直中部，触控区高 44px。
- 顶栏左侧标题，右侧按需放更新时间、刷新/自动刷新、分隔线及全局操作；不要为没有刷新能力的页面伪造刷新控件。

### 移动壳层（≤760px）

- 移除桌面侧栏，底部使用五列导航：四个最高频入口 +「更多」；根据目标业务选择入口，不照搬 GPT-Load 的业务名称。选中项浅橙底、橙色图标及文字。
- 「更多」打开底部 sheet，容纳其余导航、主题、语言与退出等全局操作。
- 处理 `env(safe-area-inset-*)`，保证内容不被底栏遮挡；顶栏标题 15px，隐藏不必要的桌面次级控件。
- 设计触控目标至少 44px；移动输入字号 16px，以避免 iOS 自动放大。使用合适的滚动容器，不让列表和页面争抢极小滚动区域。

## 6. 公共组件规格

### 按钮

| 变体 | 默认外观 | 典型用途 |
|---|---|---|
| default | surface、1px control-border、8px 圆角、轻微 control 阴影 | 次要常用操作 |
| primary | `#ff4f1f` 背景、白字；hover `#f04012` | 页面主要操作、保存 |
| outline | 透明底、accent 文字及边线 | 需强调但非主要的操作 |
| ghost | 透明无边、muted；hover 浅底及主文字 | 工具栏、图标动作 |
| brand | 透明底和 accent，hover accent-soft | 品牌强调入口 |
| danger | danger-soft 底、danger 字，hover 加强边线 | 危险操作；不是默认红底白字 |
| text | 无框、继承字体，hover 橙色下划线 | 行内文字动作 |

默认高度 36px、13px、水平内边距 12px、图标间距 6px；xxs/xs/sm 分别 24/28/32px。图标按钮等宽等高。移动常规按钮和图标按钮的触控区域达到 44px。loading 显示旋转图标、禁用重复提交并设置 `aria-busy`；disabled 不仅改变颜色，还要真正禁用。

### 字段、选择、开关

- 输入框：最小高 36px，surface 背景，1px control-border，8px 圆角，水平 padding 12px，13px 字号；移动高至少 44px、文字 16px。
- hover 边框用 control-border-hover；focus 为 accent 边线 + 3px 淡橙色 focus 阴影；错误用 danger 边线及对应红色 focus 阴影。placeholder 使用独立 placeholder 色；disabled 用 disabled 背景。
- 表单包括可见标签、可选帮助和字段级错误；label、错误通过语义属性关联输入。搜索框可放搜索图标与可访问的清除按钮。
- 分段筛选：紧凑排列，当前项 accent-soft 底、accent 文字及细橙色混合内边线；移动允许横向滚动，点击目标至少 44px。
- Switch 默认轨道 36×20px、圆形滑块 16px；小号 30×16px、滑块 12px。关闭灰色、开启橙色，移动扩大点击区域而不必放大轨道。checkbox 为 18px。
- 不依赖浏览器默认 select 外观：选择菜单与其他控件保持相同边线、圆角、字体及 focus 风格。

### 面板、卡片、徽章

- 标准 panel：surface 底、1px border、10px 圆角、无普通阴影。面板 header 标题 15px / 600，说明 12px muted；标准 body 24px 内边距，移动 16px。标题栏与主体可用细线分隔。
- compact panel 更紧凑，可省略 header 分隔线；flush panel 内容无内边距，适合列表和表格。卡片 hover 只轻微加深边框；选中使用 accent 边框，不叠加重投影。
- badge 普通高 24px，字号 12px；xs 高 20px，11px；圆角 6px。语义 badge 使用状态色文字与对应浅底，可带图标或 6px 状态点。
- 关键指标卡为少量例外：细边、10px 圆角、关键值 24px，可在右上角添加非常淡的语义色径向晕染。晕染要足够轻，不影响文字对比或变成彩色背景块。

### 表格与列表

- 工具栏通常依次为搜索、筛选、排序和主要操作；下方可有状态分段及当前筛选摘要。
- 用真实表格或具有正确角色的网格表达表头和列；表头 sticky，表头和数据共享同一个横向滚动容器，避免错列。数字列对齐，行 hover 为轻微浅底。
- 常用分页尺寸 20、50、100；分页位于列表滚动区外。筛选无结果和真正无数据必须区分；允许重置筛选。
- 有旧数据时刷新：保留内容，叠加半透明 surface 覆盖层和 spinner；首次加载使用集合加载状态。网络失败提供重试，不用空状态掩盖错误。
- 移动端将宽表重组为摘要行：突出名称、状态、关键数字和主要操作，其余通过详情进入；不要只让手机用户横向拖动完整桌面表格。

### 对话框、抽屉、反馈

- 遮罩 `rgb(20 20 18 / 35%)`。普通 dialog 最大宽 540px，左右至少 16px 安全边距、12px 圆角、dialog 阴影，桌面距顶部 `min(16dvh, 130px)`；宽 dialog 900px，sheet 660px，确认框 420px。
- 确认框垂直居中，包含明确标题、描述、对象与取消/确认操作；危险动作明确使用危险语义。提交 pending 期间防止重复触发，按目标应用的安全要求处理 Escape/遮罩关闭和焦点。
- 编辑面板可从右侧全高进入；导航抽屉宽 280px。底部 sheet 顶部圆角。移动时宽 dialog / sheet 变全屏、编辑器全宽；普通确认框仍保持适合手机的居中布局。
- Notice 使用 info/success/warning/danger 对应图标、前景和浅底；错误使用 alert 语义。全局消息顶部居中，最大宽约 480px，允许关闭或提供明确操作。
- 空态、初次加载、错误统一居中：44px 图标底座、简短标题、说明、可选重试或重置；大约 48px 垂直、24px 水平留白。不要在每个区块重复堆砌空态。
- z-index 参考：header 10、侧栏切换 20、遮罩 30、dialog 40、menu 50、tooltip 60、message 65、跳转链接 70。要结合目标项目的 stacking context 验证，不只增加任意大数值。

## 7. 典型页面组装方式

以下是可以映射到任意项目业务的**结构模式**，不是要照搬原项目的数据模型。

### 7.1 总览工作台

顶部紧凑状态条，展示总体状态及少量关键数字；下面主次两列。左侧放主要业务和操作面板，右侧放趋势、账号、健康或待办；右侧宽度 `clamp(304px, 28%, 432px)`。≤1150px 主布局改一列，右侧子面板可以两列；≤760px 变单列，次级编辑功能通过全屏面板进入。

### 7.2 资源管理列表

搜索 + 多维筛选 + 排序 + 主操作，接状态分段和筛选摘要；主体为信息密集的列表，行内展示名称、状态、关键统计和适量快捷操作。提供展开、行内切换、分页、加载覆盖及清晰的空/错误状态。手机变摘要行，不强行复制桌面所有列。

### 7.3 资源详情与编辑

详情头部包含返回、图标、名称、状态、关键元信息；宽布局左侧为主要资源，右侧约 280px 放概览/设置。复杂编辑进入右侧面板。源项目分组详情在**内容区宽度 ≤980px** 时切换为紧凑视图和分段导航；此阈值是局部容器行为，不是全局 viewport 断点。

### 7.4 数据分析页

顶部时间范围和条件筛选；接五张 KPI 卡（桌面五列，紧凑桌面三加二，手机两列并可让最后一张跨整行）；下方趋势与构成图桌面约 `2fr : minmax(300px, 1fr)`，再接排行。图表线色语义固定、面积透明度 8%–10%，图形为信息服务，不做装饰性彩色大面积填充。

### 7.5 设置页

搜索 + 左侧分类 + 右侧设置内容；桌面分类宽约 184px、紧凑桌面 160px。每项由标题、说明和对应控件组成；有草稿时出现未保存数量、放弃及主色保存操作，离开前保护。手机先显示分类列表，进入分类后展示具体设置并提供返回。

### 7.6 登录页

桌面用居中窄舞台（最大宽约 440px），品牌标识在上，卡片使用非常浅的暖色、细边和柔和阴影；输入、记住登录、错误和满宽主操作纵向排列。移动端卡片可去边框、圆角与阴影，内容靠上。使用目标项目自有品牌；吉祥物不是设计体系的必备条件。

## 8. 响应式与尺寸规则

| 阈值 | 类型与用途 |
|---|---|
| ≤420px | 极窄屏局部压缩；避免裁切、重叠和不可点击 |
| ≤760px | 手机布局：底部导航、16px 内容间距、44px 触控、表格摘要、宽弹层全屏 |
| ≤1150px | 紧凑工作区：24px 内容间距，部分多栏结构折叠 |
| ≥1700px | 宽屏：内容间距 40px |

这些是全局 viewport 规则，不是所有组件的唯一阈值。原项目还按**容器**宽度调整模型列表（≤1256px）、用量排行（≤1080px 两列、≤700px 一列）以及按内容宽度调整详情（≤980px）。目标项目应根据组件容器和内容需要实现等价行为，不要机械地只监听 window 宽度。

桌面内容默认 inset 32px、底部 36px；移动 inset 16px、底部 28px，并额外预留底栏和安全区。普通面板内边距桌面 24px / 手机 16px。内容不要统一锁进 1200px 居中容器。

## 9. 主题、动效与可访问性

- 支持 `system`、`light`、`dark`。显式主题设置 `<html data-theme="light|dark">` 和 `color-scheme`；跟随系统时移除显式主题。建议在应用初始化之前同步应用已保存的主题，避免亮暗闪烁；目标项目使用其现有存储键，不必照搬原项目键名。
- dark 使用 canvas `#202020`、sidebar `#181818`、surface `#272727`：保持可辨识的层次，不要把所有表面设成同一纯黑。核验输入边界、状态色、图表和遮罩。
- 常规颜色、背景、边框和阴影过渡为 `140ms ease`；旋转 loading 为 1s。`prefers-reduced-motion: reduce` 下停用动画和过渡。切勿用动画替代明确的状态信息。
- 保留键盘可操作性、焦点可见（2px accent outline，offset 2px）、跳转到内容、合理的 heading 层次、tooltip 与图标按钮名称、表单错误关联及对话框焦点管理。
- 检查亮暗主题下文字与控件对比度；若目标内容或平台需要调整，请优先保证可访问性，并记录相对源项目的视觉偏差。

## 10. 图表、品牌与资产

- 图表系列按第 3 节 token 统一；同一系列跨总览、趋势和构成图保持一致。图表必须有标题、单位、图例及无数据状态；状态红绿不要冒充不同数据系列。
- 原项目有单独的协议身份色；目标项目如果也需要类别标识，应建立独立类别色映射，勿混同成功、警告、错误。来源详见 `web/src/frontends/modern/styles/tokens.css`。
- 原项目品牌橙为 `#FF4F1F`，横版 Logo 为 4:1；浅底品牌字 `#1C1C1B`，深底 `#F5F5F2`。这是原项目的品牌资产约束，不是要求其他项目使用 GPT-Load 名称或标志。
- 源项目使用 Lucide；渠道和客户端图标是独立本地 SVG/WebP，部分有第三方归属与许可证。复用资产前检查权利；SVG 含渐变/clipPath 时注意多个实例的 ID 冲突。
- 系统字体会随操作系统产生差异；要追求像素近似，必须固定操作系统、字体、浏览器版本及设备像素比。

## 11. 在目标项目中的落地步骤

1. **盘点与映射**：列出现有页面、组件、颜色和断点，把其映射到本规范的壳层、列表、详情、分析、设置等模式；保留原有业务能力与无障碍语义。
2. **建立基础层**：实现 token、明暗主题、基础字体、焦点与 reduced-motion；按目标浏览器能力提供 CSS 回退。
3. **重构壳层**：实现桌面展开/折叠侧栏、顶栏、内容区、移动底栏及安全区，检查滚动和导航高亮。
4. **重构公共组件**：按钮、输入、选择器、switch、badge、panel、列表框架、分页、dialog/sheet、notice、空/加载/错误状态；旧组件可通过适配层逐步替换。
5. **套用页面模式**：先选资源列表和数据总览验证密度、图表、筛选，再扩展到详情、设置和登录；业务内容不够相同的地方遵循规则而非照抄截图。
6. **补齐交互状态**：hover、pressed、focus-visible、disabled、loading、selected、invalid；覆盖用户权限差异及危险操作确认。
7. **验收与回归**：固定数据截图、键盘和手机操作测试、业务回归。记录无法验证的环境差异，禁止用未经验证的“完全复刻”作为验收结论。

## 12. 视觉验收清单

### 基准与环境

- 来源项目静态参考：`screenshot/groups-zh-CN.png`、`screenshot/usage-zh-CN.png`；图是历史截图，不保证完全匹配当前源码。以当前源码重新渲染的截图才能作为当前版本的精确基准。
- 保持相同浏览器和版本、操作系统与字体、CSS viewport、设备像素比、语言、时区、主题、侧栏展开状态。原 PNG 为 3360×2006，**不能仅由图片像素推断 CSS viewport 和设备像素比**。
- 使用固定且脱敏的数据、固定图表序列与时间、稳定排序和分页；截图时固定或禁用随机/时间相关动效。避免把生产密钥或敏感数据写入截图。
- 两个项目业务不一致时，优先比较布局、视觉比例、控件和状态，而不是要求数据行数量或文字完全一致。

### 必测矩阵

| 维度 | 最低覆盖 |
|---|---|
| 视口 | 1440×900 桌面、1150 附近、760 两侧、390×844 手机、320px 极窄屏；另测 1700px 宽屏 |
| 布局 | 侧栏展开/收起、顶栏、列表、详情、总览、设置、登录、右侧编辑和确认弹窗 |
| 主题 | 浅色、深色、跟随系统；切换和刷新后不闪错主题 |
| 状态 | 默认、hover、焦点、禁用、加载、空数据、无搜索结果、失败、选中、错误校验 |
| 交互 | 导航、筛选、清除、分页、表格滚动、弹窗焦点与关闭、保存/取消、手机底栏和 sheet |
| 可访问性 | 纯键盘、可见焦点、屏幕阅读名称、状态不只用颜色、reduced motion、文字对比、44px 移动触控目标 |
| 业务回归 | 原有路由、权限、API、表单校验、数据呈现、危险操作与保存行为不变 |

### 判定顺序

1. 先看页面骨架、侧栏和顶栏尺寸、工作区留白及滚动方式。
2. 再看字体层级、色彩、边框、控件高度、圆角、间距和图标线宽。
3. 再看列表密度、卡片、图表、菜单及弹层；对比交互和手机重排。
4. 最后检验主题、无障碍和真实业务行为。逐项记录差异，不用一张首页截图代替完整验收。

目前仓库仅有分组、用量的现成完整桌面参考截图；其他页面、暗色及移动端如需严格截图对比，必须先用当前源码和受控数据**新建基准**。本文为源码抽取，未在本次编写中启动浏览器做渲染或逐像素验收。

## 13. 可直接复制给 AI 的重构提示词

```text
请先完整阅读 DESIGN_SYSTEM.md，并以它为目标项目的视觉规范。

任务：在不改变目标项目业务、数据、API、路由、权限和交互语义的前提下，将现有界面视觉重构为该文档中的 Coral Anchor 现代管理工作区风格。

要求：
1. 先检查当前项目结构、技术栈、组件体系和所有主要页面；列出页面与设计文档页面模式的映射。
2. 先实现语义化设计 token、亮/暗/跟随系统、字体和基础无障碍，再实现应用壳层、导航、公共组件，最后逐页迁移。优先沿用项目现有框架和可复用组件。
3. 严格参照规范中的颜色、间距、字号、控件尺寸、边框、圆角、阴影、图表色、状态和响应式；按语义使用 token，不要为单页随意创造另一套视觉。
4. 桌面保留宽工作区与高密度信息布局；手机使用底部导航、摘要列表和适合触控的弹层，而不是简单缩小桌面。
5. 不能删除或伪造业务功能，也不能将业务品牌直接换成 GPT-Load；仅复刻视觉语言。未经授权不拷贝原 Logo、吉祥物或第三方商标。
6. 覆盖 hover、焦点、选中、禁用、加载、错误、空态、权限差异、移动端和 reduced motion；保证键盘、语义和对比度。
7. 使用项目现有测试命令并完成实际界面验收。固定浏览器、视口、主题与脱敏数据，按文档矩阵对比；明确列出未验证项和必要的视觉偏差。

先阅读代码并制定改造映射，再按公共基础→页面的顺序实施；完成后汇报改动、验证结果及剩余限制。
```

## 14. 源码索引与证据边界

| 主题 | 当前工作区源码 |
|---|---|
| 视觉 token、明暗色、响应式值 | `web/src/frontends/modern/styles/tokens.css` |
| 全局样式、焦点与减少动效 | `web/src/frontends/modern/styles/base.css`、`scrollbars.css` |
| 主题预启动与运行时偏好 | `web/public/theme-bootstrap.js`、`web/src/frontends/modern/app/preferences.ts` |
| 外壳、桌面/移动导航 | `web/src/frontends/modern/layouts/AppLayout.vue`、`SidebarContent.vue`、`MobileNavigation.vue` |
| 公共控件 | `web/src/frontends/modern/components/ui/`，如 `AppButton.vue`、`AppFieldControl.vue`、`AppPanel.vue`、`AppBadge.vue`、`AppListFrame.vue`、`AppDialogContent.vue` |
| 典型页面 | `web/src/frontends/modern/features/home/`、`groups/`、`usage/`、`settings/`、`auth/`、`logs/` |
| 本地品牌资产与许可 | `web/src/frontends/modern/assets/brand/README.md`、`assets/channels/README.md` |
| 现有截图 | `screenshot/groups-zh-CN.png`、`screenshot/usage-zh-CN.png` |
| 前端边界和运行说明 | `web/README.md`、`web/package.json` |

**证据限制**：本文以当前工作区的静态源码为依据，工作区包含既有未提交改动；没有运行项目、采集实时计算样式或验证目标浏览器。规范中的组件尺寸与状态对应实际源码，示意布局和跨项目实施顺序为基于源码归纳的迁移指导，不保证其他项目未经调整即可逐像素一致。
