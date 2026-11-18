# Elasticsearch 8.15 + 官方中文分词插件 analysis-smartcn。
#
# 为什么不用 IK：IK 是第三方插件，版本必须与 ES 精确匹配，升级时要同时换 URL；
# smartcn 是 Elastic 官方插件，bin/elasticsearch-plugin 会自动取匹配版本。
# 两者都免费；IK 的分词质量略好，后续可替换，查询 DSL 不变。
FROM docker.elastic.co/elasticsearch/elasticsearch:8.15.3

RUN bin/elasticsearch-plugin install --batch analysis-smartcn
