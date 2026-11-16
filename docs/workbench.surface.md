# Workbench surface

Mode: Operate. 中文桌面管理工具，兼容窄屏，连接真实 Go API，显式区分 Mock、UNKNOWN 和已确认状态。

本文的方向契约适用于独立核心管理页和早期 demo。原生宿主内的规划、多会话、设置与 Monitor 必须服从 `DESIGN.md` 的官方界面一致性规范，不把这里的品牌侧栏、工作台标题和卡片层级带入聊天窗口。

2026-11-16 原生返修后的结构为：规划统一入口默认网页，来源/配置进入菜单；多会话在主聊天区逐次追加，主窗格跟随官方侧栏；设置在官方对话框内使用单层 11 项列表与返回导航；Monitor 默认紧凑、详情折叠。既有核心页面、开关、菜单和 KAP 调用优先复用，旧 demo 仅作参考。当前实机范围与未验收项以 `docs/VALIDATION.md` 为准。

## Direction contract

THESIS: 用一张可追踪的交接工作台组织模型、用量和任务，主要动作是预览后创建任务。
OWN-WORLD: 冷白与淡蓝灰底、深海蓝文字、蓝色操作按钮，绿色确认和琥珀未知状态；清晰表格、信息行和连续工作区。使用 Noto Sans SC 字体提供中文阅读，数字使用等宽数字。
STORY: 先看到核心与适配器状态，再选择来源，形成预览，提交并查看回执及历史。
FIRST VIEWPORT: 左侧 224px 导航，顶部服务状态；主区标题和创建任务按钮，左右不对称内容区显示任务流、最近交接和模块状态。窄屏导航折行为横向滚动，表格自行滚动，不溢出整个页面。
FORM: 候选系统依次为版本控制工作区、机房控制台、实验记录工作台、航空维护清单、编辑室流程板、文件管理器、工程计算表。采用第三项实验记录工作台，seed a16b8eff。状态变化应像记录更新，保留前后证据。借用交通板的固定列、票据的留痕、词典的阅读密度纪律；不使用地图或视频流隐喻。
FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

本轮依据用户直接开始开发的授权，由实现者决定视觉细节，直接编码，不增加付费图像或设计选择等待。签名交互：交接预览在原位展开脱敏结果和完整来源，提交后同一条记录切换为回执。
