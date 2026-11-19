# 咪咕音乐音源接入与接口文档（APK 8.9.6）

## 1. 概述

本项目已将 **咪咕音乐（Migu Music）** 与 **网易云音乐** 作为完全独立、功能对等的一等音源完整接入，支持随时在登录框、左下角菜单、搜索页与设置页无缝切换：

1. **全功能独立生态（`AudioSource = "migu"`）**：首页推荐歌单、每日推荐、风格推荐、官方/精选排行榜、歌单广场分类筛选、私人 FM 与场景电台、歌曲红心喜欢、红心计数、评论区（热门/最新/发表/点赞/删除）、歌曲百科、我的自建/收藏歌单（创建/重命名/删除/增删曲目/收藏歌单）、收藏专辑、关注歌手、最近播放/本周听歌排行/播放上报以及咪咕音乐云盘均已全面对接。
2. **网易云与咪咕严格隔离**：`MiguFallback`（网易云无版权时用咪咕换源）默认关闭；只有主动切换到咪咕音源或显式开启换源时，播放与界面才会路由至咪咕，两套音源的数据缓存与登录状态完全分离。
3. **8 档音质与逐轨实测支持**：支持 `ZQ32`（至臻母带）、`ZQ24`（24-bit 至臻）、`AV3A`（菁彩声）、`Z3D`（3D 全景）、`SQ`（无损 FLAC）、`HQ`（320k）、`PQ`（128k）、`LQ`（64k），在播控栏和播放页实时弹出当前曲目实测可用音质并支持即时切换。

---

## 2. 客户端逆向提取的官方接口

基于 `MobileMusic896_014000D` APK Chaos DEX 与线上接口实测提取并实现：

### 2.1 搜索、详情与音视频流

| 用途 | 接口地址 | 认证 / 解密说明 | 代码位置 |
| --- | --- | --- | --- |
| 全库搜索（单曲/歌手/专辑/歌单） | `https://jadeite.migu.cn/music_search/v3/search/searchAll` | Header `sign = md5(query + certMD5 + salt + ms + base64(msisdn) + ts)` | [`internal/migu/migu.go`](../internal/migu/migu.go) |
| 备用单曲搜索 / 联想词 | `https://app.c.nf.migu.cn/bmw/search/song/v1.0`、`bmw/search/suggest/v3.0` | `channel: 014000D` | [`internal/migu/migu.go`](../internal/migu/migu.go) |
| 资源详情 / 歌单曲目 / 专辑曲目 / 歌手热门 | `resourceinfo.do`、`queryMusicListSongs.do`、`queryAlbumSong`、`resource/singer/song/v1.0` | `resourceType`: `2`=歌曲、`2002`=歌手、`2003`=专辑、`2021`=歌单 | [`internal/migu/migu.go`](../internal/migu/migu.go) |
| 播放地址（PC / H5） | `strategy/pc/listen/v2.0`、`strategy/listen-url/h5/v2.4` | PC AES-CBC (`ccTWaprX2aWmTIgA`) / H5 流解密，叠加音轨 `Range` 头与时长比试听校验 | [`internal/migu/stream.go`](../internal/migu/stream.go) |
| 逐字 MRC / LRC 歌词 | `mrcUrl` / `lrcUrl` | `MrcEnDeUtil` 64-bit TEA 解密输出逐字时间轴 | [`internal/migu/lyrics.go`](../internal/migu/lyrics.go) |

### 2.2 推荐、排行榜、歌单广场与电台

| 用途 | 接口地址 | 说明 | 代码位置 |
| --- | --- | --- | --- |
| 首页推荐歌单 | `/MIGUM3.0/bmw/index-show/recommend-playlist/v3.0`、`/bmw/recommend-channel/recommend-playlist/v1.0` | 返回歌单卡片列表与封面/介绍 | [`internal/migu/discovery.go`](../internal/migu/discovery.go) |
| 排行榜索引与榜单详情 | `/MIGUM3.0/bmw/rank/rank-index/v2.0`、`/MIGUM3.0/bmw/rank/rank-info/v1.0` | 榜单内 `songData` 包含完整曲目与全音质档位信息；通过 `rank:` 前缀复用统一歌单路由 | [`internal/migu/discovery.go`](../internal/migu/discovery.go) |
| 歌单广场标签与分类筛选 | `/MIGUM3.0/v1.0/template/musiclistplaza-taglist`、`musiclistplaza-listbytag` | 对齐 推荐 / 精品 / 华语 / R&B / 轻音乐 / 电子 / 学习 / 运动 与 风格推荐标签 | [`internal/migu/discovery.go`](../internal/migu/discovery.go) |
| 每日推荐与不感兴趣 | `/MIGUM3.0/bmw/index-show/recommend-song/v1.0`、`today-recommend-playlist/v1.0`、`/resource/dislike-recommend/v1.0` | 登录后优先读取个性化日推，未登录平滑回落官方新歌/热歌每日榜单 | [`internal/migu/discovery.go`](../internal/migu/discovery.go) |
| 私人 FM 与场景电台 | `/MIGUM3.0/resource/privatefm/song/v2.0`、`/bmw/music-radio/category/list/v1.0`、`/resource/sceneradio/song-list/v2.0` | 支持登录态私人 FM 与无登录场景电台衔接，以及电台不喜欢上报 (`dislike-report/v1.0`) | [`internal/migu/radio.go`](../internal/migu/radio.go) |

### 2.3 评论、收藏、歌单管理、关注、听歌记录与云盘

| 用途 | 接口地址 | 说明 | 代码位置 |
| --- | --- | --- | --- |
| 评论列表与红心计数 | `/MIGUM3.0/user/comment/stack/v1.0`、`/MIGUM2.0/v1.0/content/queryOPNumItemsAction.do` | 读取热评/最新评论与 `keepNum` 收藏数；歌曲百科结合歌手作品与相似歌单生成 | [`internal/migu/social.go`](../internal/migu/social.go) |
| 发表/删除/点赞评论 | `/MIGUM3.0/user/api/comment/add/v2.0`、`delComment.do`、`add_collection.do` (`resourceType=08`) | 需要已登录咪咕会话 | [`internal/migu/social.go`](../internal/migu/social.go) |
| 个人中心与收藏列表 | `/MIGUM2.0/v2.0/user/getMyPage.do`、`/MIGUM2.0/v1.0/user/collections.do`、`followingSingers.do` | 同步我喜欢的音乐、自建歌单、收藏歌单（`2021\|2020`）、收藏专辑（`2003`）、关注歌手 | [`internal/migu/library.go`](../internal/migu/library.go) |
| 红心/收藏/关注/歌单增删改 | `add_collection.do` / `del_collection.do`、`user/follow.do` / `unfollow.do`、`createMusicList.do`、`musiclist/manager`、`user/api/add-music-list-song/v1.0` | 支持新建/重命名/删除歌单、添加/移除歌曲、喜欢歌曲与收藏歌单/专辑/歌手 | [`internal/migu/library.go`](../internal/migu/library.go) |
| 听歌记录、周排行与云盘 | `/user/i/record/query/v1.0`、`record/report/v1.0`、`listen-song/ranking/v1.0`、`user/cloud/music/list/v2.0`、`capacity/v1.0` | 对接最近播放、播控上报、每周排行及咪咕音乐云盘列表与容量 | [`internal/migu/library.go`](../internal/migu/library.go) |
