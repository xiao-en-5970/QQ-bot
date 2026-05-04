# map — 地图配置 + 瓦片代理

> 父 skill：`../SKILL.md`。地图后端用 [Martin](https://github.com/maplibre/martin) 做矢量瓦片服务。**前端不直连 Martin**——通过本 API 反代过去，理由是 Martin 实例对外不暴露，仅服务端可达。

## 路由表

| 方法 | 路径 | 鉴权 | Handler | 用途 |
|---|---|---|---|---|
| GET | `/api/v1/config/map` | JWT + LoadUserSchool | `controller.MapConfig` | 拿地图瓦片 URL 模板 |
| GET | `/api/v1/map/tiles/:z/:x/:y` | JWT + LoadUserSchool | `controller.MapTileProxy` | 反代瓦片二进制 |

## 详细

### GET `/config/map`

返回**经本 API 转发的瓦片 URL 模板**（前端把这个塞给地图库）。

- 入参：无
- 响应：`data: { "map_tiles_url": "<url-template>" }`，例如：
  ```
  https://api.example.com/api/v1/map/tiles/{z}/{x}/{y}
  ```
- 如果 Martin 没配置，`data.map_tiles_url` 可能是空字符串 / 空配置
- Source: `controller/map_config.go::MapConfig`

### GET `/map/tiles/:z/:x/:y`

反向代理到 Martin。

- 路径参数：标准瓦片坐标
  - `:z` zoom level（整数）
  - `:x` 列号
  - `:y` 行号
- **响应不走 JSON 信封**：
  - 成功：直接返回瓦片二进制（content-type 是 `application/x-protobuf` 或 `image/png` 等，看 Martin 配置）
  - 失败：`502` / `503` 等裸 HTTP 状态码，**没有 `code: 200, message: ..., data: ...` 信封**
- Source: `controller/map_tile_proxy.go::MapTileProxy`

## 前端集成示例（伪代码）

```js
// 1. 拉配置
const cfg = await api.get('/config/map')
const tilesUrl = cfg.data.map_tiles_url   // "https://.../api/v1/map/tiles/{z}/{x}/{y}"

// 2. 把 URL 模板交给 maplibre-gl / leaflet 等
const map = new MapLibre({
  style: {
    sources: {
      pmtiles: {
        type: 'vector',
        tiles: [tilesUrl],
        // 注意：浏览器请求瓦片时仍要带 Authorization 头，否则被 JWTAuth 挡掉
      },
    },
  },
})
```

## ⚠️ 鉴权注意

`/map/tiles/:z/:x/:y` 是 `JWTAuth` 路由，**浏览器请求瓦片时仍需带 token**。常见做法：
- 用 maplibre-gl 的 `transformRequest` 在每次瓦片请求里加上 `Authorization: Bearer <token>` 头
- 或者短期内换成 cookie session（不在当前实现范围）

如果前端忘了加 token，浏览器会看到一片瓦片都加载失败（401）。

## Source 文件

- 路由：`app/router/router.go`（`api.GET("/config/map", ...)` / `api.GET("/map/tiles/:z/:x/:y", ...)`）
- 配置 handler：`app/controller/map_config.go`
- 代理 handler：`app/controller/map_tile_proxy.go`
