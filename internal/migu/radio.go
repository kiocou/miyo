package migu

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/url"
	"strings"

	"netease-music-mygo/internal/netease"
)

// RadioStation represents a Migu scene/mood radio station.
type RadioStation struct {
	ColumnID string
	Name     string
	CoverURL string
}

// RadioCategories fetches Migu's scene and mood radio stations.
func RadioCategories(ctx context.Context) ([]RadioStation, error) {
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data map[string][]struct {
			ResID  string `json:"resId"`
			Txt    string `json:"txt"`
			Img    string `json:"img"`
			Action string `json:"action"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/bmw/music-radio/category/list/v1.0", map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	var out []RadioStation
	seen := map[string]bool{}
	for _, group := range resp.Data {
		for _, item := range group {
			id := firstNonEmpty(item.ResID, queryParam(item.Action, "id"))
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, RadioStation{
				ColumnID: id,
				Name:     item.Txt,
				CoverURL: httpsURL(item.Img),
			})
		}
	}
	return out, nil
}

// SceneRadioSongs fetches tracks for a Migu scene radio station.
func SceneRadioSongs(ctx context.Context, columnID string) ([]netease.Track, error) {
	var resp struct {
		Code string     `json:"code"`
		Info string     `json:"info"`
		Data []songItem `json:"data"`
	}
	u := appCBase + "/MIGUM3.0/resource/sceneradio/song-list/v2.0?columnId=" + url.QueryEscape(columnID) + "&pageNo=1&pageSize=20"
	if err := getJSON(ctx, u, map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	out := make([]netease.Track, 0, len(resp.Data))
	for _, s := range resp.Data {
		if tr := s.toTrack(); tr.Name != "" {
			out = append(out, tr)
		}
	}
	return out, nil
}

// PersonalFM returns Migu tracks for Personal FM (推荐/熟悉/探索), using Migu's
// privatefm endpoint when signed in and Migu's scene radio stations cleanly as fallback.
func PersonalFM(mode string) ([]netease.Track, error) {
	ctx := context.Background()
	if HasCookie() {
		var resp struct {
			Code string `json:"code"`
			Data struct {
				SongItemList []songItem `json:"songItemList"`
				List         []songItem `json:"list"`
			} `json:"data"`
		}
		q := url.Values{
			"action": {"000"},
			"type":   {"000"},
			"number": {"5"},
			"scene":  {"PRIVATE_FM"},
		}
		if err := getJSON(ctx, appCBase+"/MIGUM3.0/resource/privatefm/song/v2.0?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err == nil && resp.Code == "000000" {
			var tracks []netease.Track
			for _, list := range [][]songItem{resp.Data.SongItemList, resp.Data.List} {
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

	stations, err := RadioCategories(ctx)
	if err != nil {
		return nil, err
	}
	if len(stations) == 0 {
		return nil, errors.New("migu: no radio stations available")
	}
	var matching []RadioStation
	for _, st := range stations {
		switch mode {
		case netease.FMFamiliar:
			if strings.Contains(st.Name, "华语") || strings.Contains(st.Name, "经典") || strings.Contains(st.Name, "抒情") || strings.Contains(st.Name, "怀旧") {
				matching = append(matching, st)
			}
		case netease.FMExplore:
			if strings.Contains(st.Name, "游戏") || strings.Contains(st.Name, "电") || strings.Contains(st.Name, "BPM") || strings.Contains(st.Name, "欧美") {
				matching = append(matching, st)
			}
		default:
			if strings.Contains(st.Name, "流行") || strings.Contains(st.Name, "华语") || strings.Contains(st.Name, "开车") {
				matching = append(matching, st)
			}
		}
	}
	if len(matching) == 0 {
		matching = stations
	}
	pick := matching[rand.IntN(len(matching))]
	return SceneRadioSongs(ctx, pick.ColumnID)
}

// FMTrash reports skipping/disliking a track on Migu FM.
func FMTrash(tr netease.Track) error {
	srcID := firstNonEmpty(tr.SourceID, tr.SongID, tr.CopyrightID)
	if srcID == "" {
		return nil
	}
	body := map[string]string{
		"resourceId":   srcID,
		"resourceType": "2",
	}
	_ = postJSON(context.Background(), appCBase+"/MIGUM3.0/column/music-radio/dislike-report/v1.0", map[string]string{"channel": defaultChn}, body, nil)
	return nil
}
