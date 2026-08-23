---
status: accepted
---

# 内容 API 与 Webhook 作为可选官方插件

核心不定位为 Headless CMS；默认关闭的内容 API 插件提供版本化、只读且仅包含已发布内容的 REST/JSON 接口，默认同源且不提供 GraphQL 或公开写入。独立 Webhook 插件发送带签名的发布生命周期和评论事件，采用超时、有限重试与失败记录而不引入消息队列；RSS、站点地图和导出不依赖这些插件。
