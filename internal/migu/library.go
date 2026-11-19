package migu

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"netease-music-mygo/internal/netease"
)

func requireAuth() (string, error) {
	if !HasCookie() {
		return "", netease.ErrNotSignedIn
	}
	return CurrentUID(), nil
}

type miguColPlaylist struct {
	ID          string  `json:"id"`
	MusicListID string  `json:"musicListId"`
	PlayListID  string  `json:"playListId"`
	Title       string  `json:"title"`
	Name        string  `json:"name"`
	MusicNum    flexInt `json:"musicNum"`
	Count       flexInt `json:"count"`
	Img         string  `json:"img"`
	Cover       string  `json:"cover"`
	ImgURL      string  `json:"imgUrl"`
	ImgItem     imgItem `json:"imgItem"`
	OwnerName   string  `json:"ownerName"`
	OwnerUID    string  `json:"ownerUid"`
	PlayNum     flexInt `json:"playNum"`
	Special     string  `json:"musicListType"`
}

func (c miguColPlaylist) toPlaylist(subscribed bool) netease.Playlist {
	pid := firstNonEmpty(c.MusicListID, c.PlayListID, c.ID)
	title := firstNonEmpty(c.Title, c.Name, "咪咕歌单")
	cov := httpsURL(firstNonEmpty(c.Img, c.Cover, c.ImgURL, c.ImgItem.Img, c.ImgItem.Webp))
	n := int(c.MusicNum)
	if n == 0 {
		n = int(c.Count)
	}
	special := 0
	if strings.Contains(title, "喜欢") || strings.Contains(title, "默认收藏") || c.Special == "1" {
		special = 5
	}
	pl := netease.Playlist{
		ID:          virtualID("playlist", pid),
		Name:        title,
		CoverURL:    cov,
		PicURL:      cov,
		TrackCount:  n,
		PlayCount:   float64(c.PlayNum),
		SpecialType: special,
		Subscribed:  subscribed,
		Source:      Source,
		SourceID:    pid,
	}
	pl.Creator.Nickname = c.OwnerName
	return pl
}

type myPageMusicResp struct {
	Code string `json:"code"`
	Info string `json:"info"`
	Data struct {
		MyLikeMusicInfo struct {
			TotalCount   flexInt    `json:"totalCount"`
			PlayListID   string     `json:"playListId"`
			SongItemList []songItem `json:"songItemList"`
		} `json:"myLikeMusicInfo"`
		MyCreatedMusicLists struct {
			TotalCount          flexInt           `json:"totalCount"`
			MyCreatedMusicLists []miguColPlaylist `json:"myCreatedMusicLists"`
		} `json:"myCreatedMusicLists"`
		MyCollectedMusicInfo struct {
			TotalCount            flexInt           `json:"totalCount"`
			MyCollectedMusicLists []miguColPlaylist `json:"myCollectedMusicLists"`
		} `json:"myCollectedMusicInfo"`
		RecentPlayInfo struct {
			TotalCount     flexInt    `json:"totalCount"`
			RecentPlayList []songItem `json:"recentPlayList"`
		} `json:"recentPlayInfo"`
	} `json:"data"`
}

func fetchMyPageMusic(ctx context.Context, uid string) (*myPageMusicResp, error) {
	if uid == "" {
		return nil, errors.New("migu: missing uid")
	}
	q := url.Values{
		"userId":                     {uid},
		"queryCollectMusicListCount": {"100"},
		"queryCreateMusicListCount":  {"100"},
	}
	var resp myPageMusicResp
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/user/api/my-page-music/v2.0?"+q.Encode(), pcHeaders(), &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UserPlaylists fetches Migu's liked songs playlist, user-created playlists, and collected playlists.
func UserPlaylists() (made, saved []netease.Playlist, err error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	seenMade := map[string]bool{}
	seenSaved := map[string]bool{}
	addMade := func(pl netease.Playlist) {
		if pl.SourceID != "" && !seenMade[pl.SourceID] {
			seenMade[pl.SourceID] = true
			made = append(made, pl)
		}
	}
	addSaved := func(pl netease.Playlist) {
		if pl.SourceID != "" && !seenSaved[pl.SourceID] {
			seenSaved[pl.SourceID] = true
			saved = append(saved, pl)
		}
	}

	pageErr := error(nil)
	if uid != "" {
		var mp *myPageMusicResp
		mp, pageErr = fetchMyPageMusic(ctx, uid)
		if pageErr == nil && mp != nil {
			likeInfo := mp.Data.MyLikeMusicInfo
			if likeInfo.PlayListID != "" || likeInfo.TotalCount > 0 || len(likeInfo.SongItemList) > 0 {
				favID := firstNonEmpty(likeInfo.PlayListID, "migu-liked")
				cov := ""
				if len(likeInfo.SongItemList) > 0 {
					cov = likeInfo.SongItemList[0].toTrack().Album.PicURL
				}
				cnt := int(likeInfo.TotalCount)
				if cnt == 0 {
					cnt = len(likeInfo.SongItemList)
				}
				addMade(netease.Playlist{
					ID:          virtualID("playlist", favID),
					Name:        "我喜欢的音乐",
					CoverURL:    cov,
					PicURL:      cov,
					TrackCount:  cnt,
					SpecialType: 5,
					Source:      Source,
					SourceID:    favID,
				})
			}
			for _, c := range mp.Data.MyCreatedMusicLists.MyCreatedMusicLists {
				addMade(c.toPlaylist(false))
			}
			for _, c := range mp.Data.MyCollectedMusicInfo.MyCollectedMusicLists {
				addSaved(c.toPlaylist(true))
			}
		}
	}

	if len(made) == 0 {
		var myPage struct {
			Code string `json:"code"`
			Info string `json:"info"`
			Data struct {
				CreatedMusicLists []miguColPlaylist `json:"createdMusicLists"`
				CreateMusicLists  []miguColPlaylist `json:"createMusicLists"`
				CollectMusicLists []miguColPlaylist `json:"collectMusicLists"`
				FavoriteMusicList *miguColPlaylist  `json:"favoriteMusicList"`
			} `json:"data"`
			CreatedMusicLists []miguColPlaylist `json:"createdMusicLists"`
			CollectMusicLists []miguColPlaylist `json:"collectMusicLists"`
		}
		q := url.Values{"pageNo": {"1"}, "pageSize": {"100"}}
		if uid != "" {
			q.Set("uid", uid)
			q.Set("userid", uid)
		}
		legacyErr := getJSON(ctx, appUBase+"/MIGUM2.0/v2.0/user/getMyPage.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &myPage)
		if legacyErr == nil && (myPage.Code == "" || myPage.Code == "000000") {
			pageErr = nil
			if myPage.Data.FavoriteMusicList != nil && firstNonEmpty(myPage.Data.FavoriteMusicList.ID, myPage.Data.FavoriteMusicList.MusicListID) != "" {
				pl := myPage.Data.FavoriteMusicList.toPlaylist(false)
				pl.SpecialType = 5
				if pl.Name == "" || pl.Name == "咪咕歌单" {
					pl.Name = "我喜欢的音乐"
				}
				addMade(pl)
			}
			for _, list := range [][]miguColPlaylist{myPage.Data.CreatedMusicLists, myPage.Data.CreateMusicLists, myPage.CreatedMusicLists} {
				for _, c := range list {
					addMade(c.toPlaylist(false))
				}
			}
			for _, list := range [][]miguColPlaylist{myPage.Data.CollectMusicLists, myPage.CollectMusicLists} {
				for _, c := range list {
					addSaved(c.toPlaylist(true))
				}
			}
		} else if pageErr == nil {
			pageErr = legacyErr
		}
	}

	// Supplement collected playlists via /pc/v1.0/user/collections.do or legacy MIGUM2.0 when empty.
	if len(saved) == 0 {
		cq := url.Values{
			"userid":       {uid},
			"type":         {"1"},
			"oPType":       {"03"},
			"resourceType": {"2021|2020"},
			"pageNo":       {"1"},
			"pageSize":     {"100"},
		}
		var colResp struct {
			Code        string            `json:"code"`
			Collections []miguColPlaylist `json:"collections"`
			Data        struct {
				Collections []miguColPlaylist `json:"collections"`
			} `json:"data"`
		}
		if err2 := getJSON(ctx, appCBase+"/pc/v1.0/user/collections.do?"+cq.Encode(), pcHeaders(), &colResp); err2 == nil && colResp.Code == "000000" {
			for _, list := range [][]miguColPlaylist{colResp.Collections, colResp.Data.Collections} {
				for _, c := range list {
					addSaved(c.toPlaylist(true))
				}
			}
		} else if err2 := getJSON(ctx, appCBase+"/MIGUM2.0/v1.0/user/collections.do?"+cq.Encode(), map[string]string{"channel": defaultChn}, &colResp); err2 == nil && colResp.Code == "000000" {
			for _, list := range [][]miguColPlaylist{colResp.Collections, colResp.Data.Collections} {
				for _, c := range list {
					addSaved(c.toPlaylist(true))
				}
			}
		}
	}

	if len(made) == 0 && len(saved) == 0 && pageErr != nil {
		return nil, nil, pageErr
	}
	return made, saved, nil
}

// LikedTracks returns the user's liked Migu songs from their favorites playlist or collections.
func LikedTracks() ([]netease.Track, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	dedupe := func(list []songItem) []netease.Track {
		out := make([]netease.Track, 0, len(list))
		seen := map[int64]bool{}
		for _, s := range list {
			tr := s.toTrack()
			if tr.ID != 0 && !seen[tr.ID] {
				seen[tr.ID] = true
				out = append(out, tr)
			}
		}
		return out
	}

	if uid != "" {
		if mp, err := fetchMyPageMusic(ctx, uid); err == nil && mp != nil {
			fav := mp.Data.MyLikeMusicInfo
			if fav.PlayListID != "" {
				if pl, err := PlaylistDetail(fav.PlayListID); err == nil && pl != nil {
					if len(pl.Tracks) > 0 || fav.TotalCount == 0 {
						return pl.Tracks, nil
					}
				}
			}
			if len(fav.SongItemList) > 0 {
				return dedupe(fav.SongItemList), nil
			}
		}
	}

	q := url.Values{
		"userid":       {uid},
		"type":         {"1"},
		"OPType":       {"03"},
		"resourceType": {"2"},
		"pageNo":       {"1"},
		"pageSize":     {"200"},
	}
	var resp struct {
		Code        string     `json:"code"`
		Info        string     `json:"info"`
		Collections []songItem `json:"collections"`
		Data        struct {
			Collections []songItem `json:"collections"`
			List        []songItem `json:"list"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM2.0/v1.0/user/collections.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	var tracks []netease.Track
	seen := map[int64]bool{}
	for _, list := range [][]songItem{resp.Collections, resp.Data.Collections, resp.Data.List} {
		for _, tr := range dedupe(list) {
			if !seen[tr.ID] {
				seen[tr.ID] = true
				tracks = append(tracks, tr)
			}
		}
	}
	return tracks, nil
}

// Like likes or unlikes a Migu song using Migu web endpoints first and MIGUM2.0 fallback.
func Like(tr netease.Track, like bool) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	srcID := firstNonEmpty(tr.SourceID, tr.CopyrightID, tr.SongID)
	if srcID == "" {
		return errors.New("migu: empty track id")
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	var webErr error
	if like {
		webErr = postJSON(ctx, appCBase+"/pc/user/api/add-music-list-song/v1.0", pcHeaders(), map[string]any{
			"contentIds": []string{srcID},
		}, &resp)
	} else {
		webErr = postJSON(ctx, appCBase+"/pc/user/h5-import-musiclist/v1.0", pcHeaders(), map[string]any{
			"channel":   "23",
			"contentId": srcID,
			"songflag":  "2",
		}, &resp)
	}
	if webErr == nil {
		webErr = checkCode(resp.Code, resp.Info)
	}
	if webErr == nil {
		return nil
	}
	// Try MIGUM2.0 collection fallback when aversionid is present or PC gateway differs.
	if _, av, _, _ := authSnapshot(); av != "" {
		if err := collectAction(ctx, "2", srcID, tr.Name, tr.ArtistNames(), like); err == nil {
			return nil
		}
	}
	return webErr
}

func collectAction(ctx context.Context, resType, resID, resName, owner string, add bool) error {
	endpoint := appUBase + "/MIGUM2.0/v1.0/user/add_collection.do"
	if !add {
		endpoint = appUBase + "/MIGUM2.0/v1.0/user/del_collection.do"
	}
	q := url.Values{
		"outResourceType": {resType},
		"outResourceId":   {resID},
		"outOPType":       {"03"},
		"resourceType":    {resType},
		"resourceId":      {resID},
		"OPType":          {"03"},
	}
	if resName != "" {
		q.Set("outResourceName", resName)
	}
	if owner != "" {
		q.Set("outOwner", owner)
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	if err := getJSON(ctx, endpoint+"?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return err
	}
	return checkCode(resp.Code, resp.Info)
}

func pcCollectAction(ctx context.Context, resType, resID string, add bool) error {
	endpoint := appCBase + "/pc/v1.0/user/add_collection.do"
	if !add {
		endpoint = appCBase + "/pc/v1.0/user/del_collection.do"
	}
	q := url.Values{
		"oPType":       {"03"},
		"resourceId":   {resID},
		"resourceType": {resType},
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	err := getJSON(ctx, endpoint+"?"+q.Encode(), pcHeaders(), &resp)
	if err == nil {
		err = checkCode(resp.Code, resp.Info)
	}
	if err == nil {
		return nil
	}
	if _, av, _, _ := authSnapshot(); av != "" {
		if ferr := collectAction(ctx, resType, resID, "", "", add); ferr == nil {
			return nil
		}
	}
	return err
}

// SubscribePlaylist collects or uncollects a Migu playlist.
func SubscribePlaylist(sourceID string, subscribe bool) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	return pcCollectAction(context.Background(), "2021", sourceID, subscribe)
}

// SubscribeAlbum collects or uncollects a Migu album.
func SubscribeAlbum(sourceID string, subscribe bool) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	return pcCollectAction(context.Background(), "2003", sourceID, subscribe)
}

// SubscribedAlbums returns Migu albums collected by the signed-in user.
func SubscribedAlbums() ([]netease.AlbumProfile, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"userid":       {uid},
		"type":         {"1"},
		"OPType":       {"03"},
		"resourceType": {"2003"},
		"pageNo":       {"1"},
		"pageSize":     {"100"},
	}
	var resp struct {
		Code        string `json:"code"`
		Info        string `json:"info"`
		Collections []struct {
			ID          string  `json:"id"`
			AlbumID     string  `json:"albumId"`
			Title       string  `json:"title"`
			Name        string  `json:"name"`
			Singer      string  `json:"singer"`
			SingerID    string  `json:"singerId"`
			Img         string  `json:"img"`
			ImgItem     imgItem `json:"imgItem"`
			PublishDate string  `json:"publishDate"`
		} `json:"collections"`
	}
	pcQ := url.Values{
		"userid":       {uid},
		"type":         {"1"},
		"oPType":       {"03"},
		"resourceType": {"2003|5"},
		"pageNo":       {"1"},
		"pageSize":     {"100"},
	}
	err = getJSON(context.Background(), appCBase+"/pc/v1.0/user/collections.do?"+pcQ.Encode(), pcHeaders(), &resp)
	if err != nil || (resp.Code != "" && resp.Code != "000000") {
		if lerr := getJSON(context.Background(), appCBase+"/MIGUM2.0/v1.0/user/collections.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); lerr == nil && (resp.Code == "" || resp.Code == "000000") {
			err = nil
		} else if err == nil {
			err = checkCode(resp.Code, resp.Info)
		}
	}
	if err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	out := make([]netease.AlbumProfile, 0, len(resp.Collections))
	for _, c := range resp.Collections {
		aid := firstNonEmpty(c.AlbumID, c.ID)
		if aid == "" {
			continue
		}
		al := netease.AlbumProfile{
			Album: netease.Album{
				ID:       virtualID("album", aid),
				Name:     firstNonEmpty(c.Title, c.Name),
				PicURL:   httpsURL(firstNonEmpty(c.Img, c.ImgItem.Img, c.ImgItem.Webp)),
				Source:   Source,
				SourceID: aid,
			},
			PublishTime: parseDateMS(c.PublishDate),
		}
		for _, nm := range splitNames(c.Singer) {
			al.Artists = append(al.Artists, netease.Artist{
				ID:     virtualID("artist", nm),
				Name:   nm,
				Source: Source,
			})
		}
		out = append(out, al)
	}
	return out, nil
}

// FollowArtist follows or unfollows a Migu singer.
func FollowArtist(sourceID string, follow bool) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	endpoint := appUBase + "/MIGUM2.0/v1.0/user/follow.do"
	if !follow {
		endpoint = appUBase + "/MIGUM2.0/v1.0/user/unfollow.do"
	}
	q := url.Values{"followId": {sourceID}, "type": {"2"}}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	if err := getJSON(context.Background(), endpoint+"?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return err
	}
	return checkCode(resp.Code, resp.Info)
}

// FollowedArtists returns Migu singers followed by the signed-in user.
func FollowedArtists() ([]netease.ArtistProfile, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	type followSinger struct {
		SingerID string `json:"singerId"`
		ID       string `json:"id"`
		Name     string `json:"singerName"`
		Title    string `json:"title"`
		Singer   string `json:"singer"`
		Img      string `json:"img"`
		ImgURL   string `json:"imgUrl"`
		Avatar   string `json:"avatar"`
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			Contents []followSinger `json:"contents"`
			List     []followSinger `json:"list"`
		} `json:"data"`
	}
	if uid != "" {
		if err := getJSON(ctx, appCBase+"/MIGUM3.0/bmw/singer-index/follow/v1.0?uid="+url.QueryEscape(uid), pcHeaders(), &resp); err == nil && (resp.Code == "" || resp.Code == "000000") {
			items := resp.Data.Contents
			if len(items) == 0 {
				items = resp.Data.List
			}
			out := make([]netease.ArtistProfile, 0, len(items))
			for _, s := range items {
				sid := firstNonEmpty(s.SingerID, s.ID)
				if sid == "" {
					continue
				}
				pic := httpsURL(firstNonEmpty(s.Img, s.ImgURL, s.Avatar))
				out = append(out, netease.ArtistProfile{
					Artist: netease.Artist{
						ID:       virtualID("artist", sid),
						Name:     firstNonEmpty(s.Name, s.Title, s.Singer),
						Source:   Source,
						SourceID: sid,
					},
					Avatar: pic,
					PicURL: pic,
				})
			}
			return out, nil
		}
	}
	q := url.Values{"uid": {uid}, "pageNo": {"1"}, "pageSize": {"100"}}
	if err := getJSON(ctx, appCBase+"/MIGUM2.0/v1.0/user/followingSingers.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	out := make([]netease.ArtistProfile, 0, len(resp.Data.List))
	for _, s := range resp.Data.List {
		sid := firstNonEmpty(s.SingerID, s.ID)
		pic := httpsURL(firstNonEmpty(s.Img, s.ImgURL, s.Avatar))
		out = append(out, netease.ArtistProfile{
			Artist: netease.Artist{
				ID:       virtualID("artist", sid),
				Name:     firstNonEmpty(s.Name, s.Title, s.Singer),
				Source:   Source,
				SourceID: sid,
			},
			Avatar: pic,
			PicURL: pic,
		})
	}
	return out, nil
}

// CreatePlaylist creates a new Migu playlist with the given title.
func CreatePlaylist(name string) (*netease.Playlist, error) {
	if _, err := requireAuth(); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("migu: empty playlist name")
	}
	ctx := context.Background()
	var resp struct {
		Code        string `json:"code"`
		Info        string `json:"info"`
		MusicListID string `json:"musicListId"`
		Data        struct {
			MusicListID string `json:"musicListId"`
			PlayListID  string `json:"playListId"`
			ID          string `json:"id"`
		} `json:"data"`
	}
	err := postJSON(ctx, appCBase+"/pc/open/api/music-list/add/v2.0", pcHeaders(), map[string]string{
		"title":   name,
		"channel": "23",
		"type":    "self_build",
	}, &resp)
	if err == nil {
		err = checkCode(resp.Code, resp.Info)
	}
	if err != nil {
		if _, av, _, _ := authSnapshot(); av != "" {
			q := url.Values{"title": {name}, "name": {name}}
			if ferr := getJSON(ctx, appUBase+"/MIGUM2.0/v1.0/user/createMusicList.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); ferr == nil {
				if cerr := checkCode(resp.Code, resp.Info); cerr == nil {
					err = nil
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	pid := firstNonEmpty(resp.MusicListID, resp.Data.MusicListID, resp.Data.PlayListID, resp.Data.ID)
	return &netease.Playlist{
		ID:       virtualID("playlist", pid),
		Name:     name,
		Source:   Source,
		SourceID: pid,
	}, nil
}

// RenamePlaylist renames a Migu playlist.
func RenamePlaylist(sourceID, name string) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	err := postJSON(ctx, appCBase+"/pc/user/h5-import-musiclist/v1.0", pcHeaders(), map[string]string{
		"title":    name,
		"channel":  "23",
		"id":       sourceID,
		"songflag": "0",
	}, &resp)
	if err == nil {
		err = checkCode(resp.Code, resp.Info)
	}
	if err != nil {
		if _, av, _, _ := authSnapshot(); av != "" {
			body := map[string]string{
				"musicListId": sourceID,
				"title":       name,
				"opType":      "update",
			}
			if ferr := postJSON(ctx, appCBase+"/MIGUM3.0/musiclist/manager", map[string]string{"channel": defaultChn}, body, &resp); ferr == nil && checkCode(resp.Code, resp.Info) == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// DeletePlaylist deletes a user-created Migu playlist.
func DeletePlaylist(sourceID string) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	q := url.Values{"channel": {"23"}, "id": {sourceID}}
	err := getJSON(ctx, appCBase+"/pc/v1.0/user/deleteMusicList.do?"+q.Encode(), pcHeaders(), &resp)
	if err == nil {
		err = checkCode(resp.Code, resp.Info)
	}
	if err != nil {
		if _, av, _, _ := authSnapshot(); av != "" {
			body := map[string]string{
				"musicListId": sourceID,
				"opType":      "delete",
			}
			if ferr := postJSON(ctx, appCBase+"/MIGUM3.0/musiclist/manager", map[string]string{"channel": defaultChn}, body, &resp); ferr == nil && checkCode(resp.Code, resp.Info) == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// AddToPlaylist appends song content IDs to a Migu playlist.
func AddToPlaylist(playlistID string, songIDs ...string) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	if len(songIDs) == 0 {
		return nil
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			FailContentIDs           string   `json:"failContentIds"`
			Fail2RepeatContentIDList []string `json:"fail2RepeatContentIdList"`
			SuccessNum               flexInt  `json:"successNum"`
		} `json:"data"`
	}
	body := map[string]any{
		"id":         playlistID,
		"contentIds": songIDs,
	}
	if err := postJSON(ctx, appCBase+"/pc/user/api/add-music-list-song/v1.0", pcHeaders(), body, &resp); err != nil {
		return err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return err
	}
	if int(resp.Data.SuccessNum) == 0 && (resp.Data.FailContentIDs != "" || len(resp.Data.Fail2RepeatContentIDList) > 0) {
		return netease.ErrInPlaylist
	}
	return nil
}

// RemoveFromPlaylist removes song content IDs from a Migu playlist.
func RemoveFromPlaylist(playlistID string, songIDs ...string) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	err := postJSON(ctx, appCBase+"/pc/user/h5-import-musiclist/v1.0", pcHeaders(), map[string]any{
		"channel":   "23",
		"songflag":  "2",
		"contentId": strings.Join(songIDs, "_"),
		"id":        playlistID,
	}, &resp)
	if err == nil {
		err = checkCode(resp.Code, resp.Info)
	}
	if err != nil {
		if _, av, _, _ := authSnapshot(); av != "" {
			body := map[string]any{
				"id":         playlistID,
				"contentIds": songIDs,
				"opType":     "deleteSong",
			}
			if ferr := postJSON(ctx, appCBase+"/MIGUM3.0/musiclist/manager", map[string]string{"channel": defaultChn}, body, &resp); ferr == nil && checkCode(resp.Code, resp.Info) == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

// RecentPlays returns the user's Migu play history.
func RecentPlays(limit int) ([]netease.Play, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	ctx := context.Background()
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			List []struct {
				Song       songItem `json:"song"`
				ContentID  string   `json:"contentId"`
				SongName   string   `json:"songName"`
				CreateTime flexInt  `json:"createTime"`
			} `json:"list"`
		} `json:"data"`
	}
	u := fmt.Sprintf("%s/MIGUM3.0/user/i/record/query/v1.0?pageNo=1&pageSize=%d", appCBase, limit)
	if err := getJSON(ctx, u, map[string]string{"channel": defaultChn}, &resp); err == nil && checkCode(resp.Code, resp.Info) == nil {
		out := make([]netease.Play, 0, len(resp.Data.List))
		for _, it := range resp.Data.List {
			tr := it.Song.toTrack()
			if tr.Name == "" {
				tr = (songItem{ContentID: it.ContentID, SongName: it.SongName}).toTrack()
			}
			at := time.UnixMilli(int64(it.CreateTime))
			if int64(it.CreateTime) == 0 {
				at = time.Now()
			}
			out = append(out, netease.Play{Track: tr, At: at, Device: "pc"})
		}
		return out, nil
	}
	if uid != "" {
		if mp, err := fetchMyPageMusic(ctx, uid); err == nil && mp != nil {
			list := mp.Data.RecentPlayInfo.RecentPlayList
			if len(list) > limit {
				list = list[:limit]
			}
			out := make([]netease.Play, 0, len(list))
			now := time.Now()
			for i, s := range list {
				out = append(out, netease.Play{
					Track:  s.toTrack(),
					At:     now.Add(-time.Duration(i) * time.Minute),
					Device: "pc",
				})
			}
			return out, nil
		}
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	return nil, errors.New("migu recent plays: unavailable")
}

// Scrobble reports a Migu playback event to /user/i/record/report/v1.0.
func Scrobble(tr netease.Track, seconds int) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	srcID := firstNonEmpty(tr.SourceID, tr.SongID, tr.CopyrightID)
	if srcID == "" {
		return nil
	}
	body := map[string]any{
		"actionType": "listen",
		"duration":   seconds,
		"reportItems": []map[string]any{
			{
				"resourceId":   srcID,
				"resourceType": "2",
			},
		},
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	if err := postJSON(context.Background(), appCBase+"/MIGUM3.0/user/i/record/report/v1.0", map[string]string{"channel": defaultChn}, body, &resp); err != nil {
		return err
	}
	return checkCode(resp.Code, resp.Info)
}

// WeekRanking returns Migu's personal listening ranking for the past 7 days.
func WeekRanking() ([]netease.Ranked, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	body := map[string]string{
		"uid":  uid,
		"type": "1",
	}
	type rankItem struct {
		Song      songItem `json:"song"`
		PlayCount int      `json:"playCount"`
		Score     int      `json:"score"`
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			List          []rankItem `json:"list"`
			SongRankInfos []rankItem `json:"songRankInfos"`
		} `json:"data"`
	}
	if err := postJSON(context.Background(), appCBase+"/MIGUM3.0/bmw/listen-song/ranking/v1.0", map[string]string{"channel": defaultChn}, body, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	items := resp.Data.SongRankInfos
	if len(items) == 0 {
		items = resp.Data.List
	}
	out := make([]netease.Ranked, 0, len(items))
	for i, it := range items {
		score := it.Score
		if score == 0 && len(items) > 0 {
			score = max(1, 100-i*4)
		}
		out = append(out, netease.Ranked{
			Track: it.Song.toTrack(),
			Plays: it.PlayCount,
			Score: score,
		})
	}
	return out, nil
}

// CloudSongs lists songs in the signed-in user's Migu Music Cloud Drive.
func CloudSongs() (*netease.CloudDrive, error) {
	if _, err := requireAuth(); err != nil {
		return nil, err
	}
	ctx := context.Background()
	var listResp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			List []struct {
				songItem
				FileName string `json:"fileName"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/user/cloud/music/list/v2.0?pageNo=1&pageSize=200", map[string]string{"channel": defaultChn}, &listResp); err != nil {
		return nil, err
	}
	if err := checkCode(listResp.Code, listResp.Info); err != nil {
		return nil, err
	}
	drive := &netease.CloudDrive{}
	for _, it := range listResp.Data.List {
		tr := it.songItem.toTrack()
		if tr.Name == "" {
			tr.Name = strings.TrimSpace(it.FileName)
		}
		if tr.ID == 0 && tr.SourceID == "" {
			tr.Source = Source
			tr.SourceID = tr.Name
			tr.ID = virtualID("cloud", tr.Name)
		}
		drive.Songs = append(drive.Songs, tr)
	}
	var capResp struct {
		Code string `json:"code"`
		Data struct {
			UseCapacity   flexInt `json:"useCapacity"`
			TotalCapacity flexInt `json:"totalCapacity"`
		} `json:"data"`
	}
	if err := getJSON(ctx, appCBase+"/MIGUM3.0/user/cloud/music/capacity/v1.0", map[string]string{"channel": defaultChn}, &capResp); err == nil && capResp.Code == "000000" {
		drive.Used = int64(capResp.Data.UseCapacity)
		drive.Max = int64(capResp.Data.TotalCapacity)
	}
	return drive, nil
}

// ParseID splits a string like "12345" or "migu:12345" cleanly.
func parseCommentCount(s string) int {
	v, _ := strconv.Atoi(strings.TrimSpace(s))
	return v
}
