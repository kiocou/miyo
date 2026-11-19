package migu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"netease-music-mygo/internal/netease"
)

// PlazaTag represents a Migu playlist plaza category tag.
type PlazaTag struct {
	ID       string
	Name     string
	Category string
}

type plazaCard struct {
	View      string `json:"view"`
	ViewID    string `json:"viewId"`
	Action    string `json:"action"`
	ActionURL string `json:"actionUrl"`
	ResID     string `json:"resId"`
	Title     string `json:"title"`
	SubTitle  string `json:"subTitle"`
	Txt       string `json:"txt"`
	Txt2      string `json:"txt2"`
	Img       string `json:"img"`
	ImageURL  string `json:"imageUrl"`
	PlayNum   string `json:"playNum"`
	LogEvent  struct {
		ContentID   string `json:"contentId"`
		ContentName string `json:"contentName"`
		ResourceID  string `json:"resourceId"`
	} `json:"logEvent"`
}

func (c plazaCard) toPlaylist() (netease.Playlist, bool) {
	id := firstNonEmpty(c.ResID, c.LogEvent.ContentID, c.LogEvent.ResourceID)
	if id == "" {
		id = queryParam(firstNonEmpty(c.Action, c.ActionURL), "id")
	}
	name := firstNonEmpty(c.Txt, c.Title, c.LogEvent.ContentName)
	if id == "" || name == "" {
		return netease.Playlist{}, false
	}
	cover := httpsURL(firstNonEmpty(c.Img, c.ImageURL))
	pl := netease.Playlist{
		ID:          virtualID("playlist", id),
		Name:        name,
		CoverURL:    cover,
		PicURL:      cover,
		Description: c.Txt2,
		PlayCount:   parseChineseCount(c.PlayNum),
		Source:      Source,
		SourceID:    id,
	}
	if c.SubTitle != "" && c.SubTitle != c.Txt2 {
		pl.Creator.Nickname = c.SubTitle
	}
	return pl, true
}

func queryParam(raw, key string) string {
	_, q, ok := strings.Cut(raw, "?")
	if !ok {
		return ""
	}
	vals, err := url.ParseQuery(q)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(vals.Get(key))
}

func parseChineseCount(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(s, "+"))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "亿"):
		mult = 1e8
		s = strings.TrimSuffix(s, "亿")
	case strings.HasSuffix(s, "万"), strings.HasSuffix(s, "w"), strings.HasSuffix(s, "W"):
		mult = 1e4
		s = strings.TrimRight(s, "万wW")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v * mult
}

// FeaturedPlaylists returns Migu's home recommended playlists.
func FeaturedPlaylists(limit int) ([]netease.Playlist, error) {
	return FeaturedPlaylistsContext(context.Background(), limit)
}

// FeaturedPlaylistsContext is FeaturedPlaylists with cancellation support.
func FeaturedPlaylistsContext(ctx context.Context, limit int) ([]netease.Playlist, error) {
	if limit <= 0 {
		limit = 30
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			PlayLists []plazaCard `json:"playLists"`
		} `json:"data"`
	}
	u := fmt.Sprintf("%s/MIGUM3.0/bmw/index-show/recommend-playlist/v3.0?pageNo=1&pageSize=%d", appCBase, limit)
	err := getJSON(ctx, u, map[string]string{"channel": defaultChn}, &resp)
	var out []netease.Playlist
	seen := map[string]bool{}
	if err == nil {
		for _, c := range resp.Data.PlayLists {
			if pl, ok := c.toPlaylist(); ok && !seen[pl.SourceID] {
				seen[pl.SourceID] = true
				out = append(out, pl)
			}
		}
	}
	if len(out) < limit {
		var chResp struct {
			Code string `json:"code"`
			Data struct {
				Contents []struct {
					View     string      `json:"view"`
					Contents []plazaCard `json:"contents"`
				} `json:"contents"`
			} `json:"data"`
		}
		if err2 := getJSON(ctx, appCBase+"/MIGUM3.0/bmw/recommend-channel/recommend-playlist/v1.0", map[string]string{"channel": defaultChn}, &chResp); err2 == nil {
			for _, g := range chResp.Data.Contents {
				for _, c := range g.Contents {
					if pl, ok := c.toPlaylist(); ok && !seen[pl.SourceID] {
						seen[pl.SourceID] = true
						out = append(out, pl)
						if len(out) >= limit {
							return out, nil
						}
					}
				}
			}
		} else if len(out) == 0 && err != nil {
			return nil, err
		}
	}
	if len(out) == 0 && err != nil {
		return nil, err
	}
	return out, nil
}

// RecommendedPlaylists returns personalized playlists when signed in or plaza recommendations.
func RecommendedPlaylists() ([]netease.Playlist, error) {
	return FeaturedPlaylists(20)
}

// Charts returns Migu's official and featured charts with preview songs on top charts.
func Charts() ([]netease.Playlist, error) {
	return ChartsContext(context.Background())
}

// ChartsContext is Charts with cancellation support.
func ChartsContext(ctx context.Context) ([]netease.Playlist, error) {
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			Contents []struct {
				Style    string `json:"style"`
				Contents []struct {
					RankID   string `json:"rankId"`
					RankName string `json:"rankName"`
					ImageURL string `json:"imageUrl"`
				} `json:"contents"`
			} `json:"contents"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/bmw/rank/rank-index/v2.0", map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	var out []netease.Playlist
	seen := map[string]bool{}
	for _, grp := range resp.Data.Contents {
		for _, item := range grp.Contents {
			id := strings.TrimSpace(item.RankID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			srcID := "rank:" + id
			cover := httpsURL(item.ImageURL)
			out = append(out, netease.Playlist{
				ID:         virtualID("playlist", srcID),
				Name:       item.RankName,
				CoverURL:   cover,
				PicURL:     cover,
				UpdateFreq: grp.Style,
				Source:     Source,
				SourceID:   srcID,
			})
		}
	}
	// Populate preview tracks and descriptions on the top 4 official charts concurrently.
	previewN := min(4, len(out))
	var wg sync.WaitGroup
	for i := range previewN {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rankID := strings.TrimPrefix(out[idx].SourceID, "rank:")
			if detail, err := chartDetailPage(ctx, rankID, 3); err == nil && detail != nil {
				if detail.UpdateFreq != "" {
					out[idx].UpdateFreq = detail.UpdateFreq
				}
				if detail.Description != "" {
					out[idx].Description = detail.Description
				}
				for j := 0; j < len(detail.Tracks) && j < 3; j++ {
					tr := detail.Tracks[j]
					out[idx].Preview = append(out[idx].Preview, netease.ChartLine{
						First:  tr.Name,
						Second: tr.ArtistNames(),
					})
				}
			}
		}(i)
	}
	wg.Wait()
	return out, nil
}

// ChartDetail fetches a Migu chart by its rank ID (without the "rank:" prefix).
func ChartDetail(rankID string) (*netease.Playlist, error) {
	return chartDetailPage(context.Background(), rankID, 100)
}

func chartDetailPage(ctx context.Context, rankID string, pageSize int) (*netease.Playlist, error) {
	rankID = strings.TrimSpace(strings.TrimPrefix(rankID, "rank:"))
	if rankID == "" {
		return nil, errors.New("migu: empty rank id")
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			Title      string `json:"title"`
			Desc       string `json:"desc"`
			UpdateTime string `json:"updateTime"`
			TitlePic   string `json:"titlePic"`
			TotalCount int    `json:"totalCount"`
			Contents   []struct {
				ResID       string   `json:"resId"`
				SongID      string   `json:"songId"`
				CopyrightID string   `json:"copyrightId"`
				Img         string   `json:"img"`
				Txt         string   `json:"txt"`
				Txt2        string   `json:"txt2"`
				Txt3        string   `json:"txt3"`
				Qualities   []string `json:"qualities"`
				SongData    string   `json:"songData"`
			} `json:"contents"`
		} `json:"data"`
	}
	u := fmt.Sprintf("%s/MIGUM3.0/bmw/rank/rank-info/v1.0?rankId=%s&pageNo=1&pageSize=%d",
		appCBase, url.QueryEscape(rankID), pageSize)
	if err := getJSON(ctx, u, map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	srcID := "rank:" + rankID
	cover := httpsURL(resp.Data.TitlePic)
	pl := &netease.Playlist{
		ID:          virtualID("playlist", srcID),
		Name:        firstNonEmpty(resp.Data.Title, "咪咕榜单"),
		Description: resp.Data.Desc,
		CoverURL:    cover,
		PicURL:      cover,
		TrackCount:  resp.Data.TotalCount,
		UpdateFreq:  resp.Data.UpdateTime,
		Source:      Source,
		SourceID:    srcID,
	}
	if pl.UpdateFreq != "" {
		pl.UpdateFreq = "更新于 " + pl.UpdateFreq
	}
	for _, it := range resp.Data.Contents {
		var tr netease.Track
		if it.SongData != "" {
			var si songItem
			if json.Unmarshal([]byte(it.SongData), &si) == nil {
				if si.ContentID == "" {
					si.ContentID = it.ResID
				}
				if si.CopyrightID == "" {
					si.CopyrightID = it.CopyrightID
				}
				if si.SongID == "" {
					si.SongID = it.SongID
				}
				if si.ImgURL == "" {
					si.ImgURL = it.Img
				}
				tr = si.toTrack()
			}
		}
		if tr.Name == "" {
			si := songItem{
				ContentID:   it.ResID,
				CopyrightID: it.CopyrightID,
				SongID:      it.SongID,
				SongName:    it.Txt,
				Singer:      it.Txt2,
				AlbumName:   it.Txt3,
				ImgURL:      it.Img,
			}
			for _, q := range it.Qualities {
				q = strings.ToUpper(strings.TrimPrefix(strings.TrimPrefix(q, "i"), "a"))
				si.AudioFormats = append(si.AudioFormats, formatEntry{FormatType: q})
			}
			tr = si.toTrack()
		}
		if tr.Name != "" {
			pl.Tracks = append(pl.Tracks, tr)
		}
	}
	if pl.CoverURL == "" && len(pl.Tracks) > 0 {
		pl.CoverURL = pl.Tracks[0].Album.PicURL
		pl.PicURL = pl.CoverURL
	}
	return pl, nil
}

// PlaylistTags returns all playlist plaza tags grouped by category.
func PlaylistTags(ctx context.Context) ([]PlazaTag, error) {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Header struct {
				Title string `json:"title"`
			} `json:"header"`
			Content []struct {
				Texts []string `json:"texts"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/v1.0/template/musiclistplaza-taglist", map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	var out []PlazaTag
	for _, g := range resp.Data {
		for _, it := range g.Content {
			if len(it.Texts) >= 2 {
				out = append(out, PlazaTag{
					Name:     strings.TrimSpace(it.Texts[0]),
					ID:       strings.TrimSpace(it.Texts[1]),
					Category: strings.TrimSpace(g.Header.Title),
				})
			}
		}
	}
	return out, nil
}

func resolveTagID(ctx context.Context, category string) (string, error) {
	tags, err := PlaylistTags(ctx)
	if err != nil {
		return "", err
	}
	category = strings.TrimSpace(category)
	var candidates []string
	switch category {
	case "", "精品", "官方":
		candidates = []string{"官方", "网络热歌", "流行"}
	case "华语":
		candidates = []string{"华语", "国语", "流行"}
	case "轻音乐":
		candidates = []string{"轻音乐", "纯音乐", "治愈"}
	case "R&B/Soul", "R&B":
		candidates = []string{"R&B", "欧美", "流行"}
	default:
		candidates = []string{category}
	}
	for _, c := range candidates {
		for _, t := range tags {
			if strings.EqualFold(t.Name, c) {
				return t.ID, nil
			}
		}
		for _, t := range tags {
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(c)) {
				return t.ID, nil
			}
		}
	}
	if len(tags) > 0 {
		return tags[0].ID, nil
	}
	return "", errors.New("migu: no plaza tags found")
}

// CategoryPlaylists returns Migu plaza playlists matching category.
func CategoryPlaylists(category string, limit int) ([]netease.Playlist, error) {
	ctx := context.Background()
	tagID, err := resolveTagID(ctx, category)
	if err != nil {
		return FeaturedPlaylistsContext(ctx, limit)
	}
	return playlistsByTag(ctx, tagID, limit)
}

// HighQualityPlaylists returns official/featured Migu plaza playlists.
func HighQualityPlaylists(limit int) ([]netease.Playlist, error) {
	return CategoryPlaylists("官方", limit)
}

func playlistsByTag(ctx context.Context, tagID string, limit int) ([]netease.Playlist, error) {
	if limit <= 0 {
		limit = 30
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			ContentItemList struct {
				ItemList []plazaCard `json:"itemList"`
			} `json:"contentItemList"`
		} `json:"data"`
	}
	u := fmt.Sprintf("%s/MIGUM3.0/v1.0/template/musiclistplaza-listbytag?tagId=%s&pageNo=1&pageSize=%d",
		appCBase, url.QueryEscape(tagID), limit)
	if err := getJSON(ctx, u, map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	var out []netease.Playlist
	for _, c := range resp.Data.ContentItemList.ItemList {
		if pl, ok := c.toPlaylist(); ok {
			out = append(out, pl)
		}
	}
	return out, nil
}

// DailySongs returns personalized daily songs when authenticated, or Migu's curated hot songs.
func DailySongs() ([]netease.Track, error) {
	ctx := context.Background()
	if HasCookie() {
		var resp struct {
			Code string `json:"code"`
			Data struct {
				SongList []songItem `json:"songList"`
				Songs    []songItem `json:"songs"`
				List     []songItem `json:"list"`
				Contents []struct {
					SongItem songItem `json:"song"`
					SongData string   `json:"songData"`
				} `json:"contents"`
			} `json:"data"`
		}
		for _, endpoint := range []string{
			appCBase + "/MIGUM3.0/bmw/index-show/recommend-song/v1.0?pageNo=1&pageSize=30",
			appCBase + "/MIGUM3.0/bmw/index-show/today-recommend-playlist/v1.0?pageNo=1&pageSize=30",
		} {
			if err := getJSON(ctx, endpoint, map[string]string{"channel": defaultChn}, &resp); err == nil && resp.Code == "000000" {
				var tracks []netease.Track
				for _, list := range [][]songItem{resp.Data.SongList, resp.Data.Songs, resp.Data.List} {
					for _, s := range list {
						if tr := s.toTrack(); tr.Name != "" {
							tracks = append(tracks, tr)
						}
					}
				}
				if len(tracks) > 0 {
					return tracks, nil
				}
			}
		}
	}
	// Curated daily tracks from Migu's official New Song & Hot Song charts.
	for _, rankID := range []string{"27553319", "83048887"} {
		if ch, err := chartDetailPage(ctx, rankID, 30); err == nil && ch != nil && len(ch.Tracks) > 0 {
			return ch.Tracks, nil
		}
	}
	return nil, errors.New("migu: unable to load daily songs")
}

// DailyDislike reports disliking a recommended Migu track and tries to return a replacement.
func DailyDislike(tr netease.Track) (netease.Track, bool, error) {
	srcID := firstNonEmpty(tr.SourceID, tr.SongID, tr.CopyrightID)
	if srcID == "" {
		return netease.Track{}, false, nil
	}
	body := map[string]string{
		"resourceId":   srcID,
		"resourceType": "2",
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	_ = postJSON(context.Background(), appCBase+"/MIGUM3.0/resource/dislike-recommend/v1.0", map[string]string{"channel": defaultChn}, body, &resp)
	return netease.Track{}, false, nil
}

// StyleCategories returns Migu plaza tag groups shaped as netease.StyleCategory.
func StyleCategories() ([]netease.StyleCategory, error) {
	tags, err := PlaylistTags(context.Background())
	if err != nil {
		return nil, err
	}
	var cats []netease.StyleCategory
	idx := map[string]int{}
	for _, t := range tags {
		pos, ok := idx[t.Category]
		if !ok {
			pos = len(cats)
			idx[t.Category] = pos
			cid := virtualID("stylecat", t.Category)
			if cid < 0 {
				cid = -cid
			}
			cats = append(cats, netease.StyleCategory{
				ID:   cid,
				Name: t.Category,
			})
		}
		tid, err := strconv.ParseInt(t.ID, 10, 64)
		if err != nil {
			tid = -virtualID("styletag", t.ID)
		}
		cats[pos].Tags = append(cats[pos].Tags, netease.StyleTag{
			ID:       tid,
			Name:     t.Name,
			Category: cats[pos].ID,
		})
	}
	return cats, nil
}

// DailyStyleSongs returns songs from playlists matching category/tag name on Migu.
func DailyStyleSongs(tagName string) (netease.StyleSongs, error) {
	cats, err := StyleCategories()
	if err != nil || len(cats) == 0 {
		return netease.StyleSongs{}, err
	}
	picked := cats[0]
	if tagName == "" && len(picked.Tags) > 0 {
		tagName = picked.Tags[0].Name
	} else if tagName != "" {
		for _, c := range cats {
			for _, t := range c.Tags {
				if strings.EqualFold(t.Name, tagName) {
					picked = c
					picked.Tags = []netease.StyleTag{t}
					break
				}
			}
		}
	}
	lists, err := CategoryPlaylists(tagName, 3)
	if err != nil {
		return netease.StyleSongs{Picked: picked}, err
	}
	var songs []netease.Track
	seen := map[int64]bool{}
	for i := 0; i < len(lists) && i < 2; i++ {
		if pl, err := PlaylistDetail(lists[i].SourceID); err == nil && pl != nil {
			for _, tr := range pl.Tracks {
				if !seen[tr.ID] {
					seen[tr.ID] = true
					songs = append(songs, tr)
				}
				if len(songs) >= 30 {
					break
				}
			}
		}
	}
	return netease.StyleSongs{Picked: picked, Songs: songs}, nil
}
