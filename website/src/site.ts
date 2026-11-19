import { appName } from '../project'
export { repo } from '../project'

export type Asset = { name: string; size: number; browser_download_url: string }
export type Release = { tag_name: string; published_at: string; assets: Asset[] }

declare const __RELEASE__: Release
// The latest release, as it was when the site was built (see vite.config.ts).
export const release = __RELEASE__
export const mygo = 'https://mygo.egoist.dev'

export const site = {
  name: `${appName} (Miyo)`,
  title: `${appName} (Miyo) — 双音源原生音乐桌面客户端`,
  description:
    '用 Go 与 MyGo 写成的开源第三方音乐桌面客户端，支持网易云音乐与咪咕音乐双音源。原生界面，没有 WebView；边下边播、高解析度音质、逐字歌词与「律动」、液态玻璃视效。',
}
