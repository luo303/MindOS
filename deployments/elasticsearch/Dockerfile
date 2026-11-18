# Elasticsearch 8.15 + 官方中文分词插件 analysis-smartcn。
#
# 为什么不用 IK：IK 是第三方插件，版本必须与 ES 精确匹配，升级时要同时换 URL；
# smartcn 是 Elastic 官方插件，bin/elasticsearch-plugin 会自动取匹配版本。
# 两者都免费；IK 的分词质量略好，后续可替换，查询 DSL 不变。
#
# 用 Docker Hub 的官方 elasticsearch 镜像，而不是 docker.elastic.co：
# 前者的拉取会经过本机的 registry mirror，后者不会。镜像内容一致，
# 只是分发渠道不同；本地先 docker pull elasticsearch:8.15.3 即可。
FROM elasticsearch:8.15.3

RUN bin/elasticsearch-plugin install --batch analysis-smartcn
