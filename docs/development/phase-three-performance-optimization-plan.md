# 公开读取性能：历史方案摘要

本页压缩保留早期计划的范围。具体实现与验证以代码及实施记录为准；旧待办不表示当前缺陷，也不承诺后续交付。

## 范围

- 用 10,000 篇文章数据区分 SQL、连接等待、渲染与缓存开销。
- 分类/标签发布投影、并发缓存 miss 合并与有界正文缓存。
- 搜索同步从请求移到后台，按有限批次可恢复重建。
- 比较 Reader=2/4/8，保留一个写连接和既定内存预算。

## 参考

- [阶段三：10,000 篇文章并发实测与瓶颈报告](../progress/phase-three-concurrency-load-report.md)
- [阶段三性能优化实施与验收报告](../progress/phase-three-performance-implementation.md)
- [当前使用说明](../usage.md)与[架构](../architecture.md)
- [原始详细计划](https://github.com/Dragonzsl/blog-project/blob/60b3262/docs/development/phase-three-performance-optimization-plan.md)
