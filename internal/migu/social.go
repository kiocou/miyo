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

type miguCommentItem struct {
	CommentID   string `json:"commentId"`
	CommentInfo string `json:"commentInfo"`
	CommentTime string `json:"commentTime"`
	HaveThumb   string `json:"haveThumb"`
	User        struct {
		UserID     string `json:"userId"`
		NickName   string `json:"nickName"`
		BigIcon    string `json:"bigIcon"`
		MiddleIcon string `json:"middleIcon"`
		SmallIcon  string `json:"smallIcon"`
	} `json:"user"`
	OpNumItem struct {
		ThumbNum    int `json:"thumbNum"`
		ThumbNumAlt int `json:"commentNum"`
	} `json:"opNumItem"`
}

func (m miguCommentItem) toComment() netease.Comment {
	var c netease.Comment
	if id, err := strconv.ParseInt(m.CommentID, 10, 64); err == nil && id > 0 {
		c.ID = id
	} else {
		c.ID = -virtualID("comment", m.CommentID)
	}
	if uid, err := strconv.ParseInt(m.User.UserID, 10, 64); err == nil {
		c.User.ID = uid
	} else {
		c.User.ID = -virtualID("user", m.User.UserID)
	}
	c.User.Nickname = m.User.NickName
	c.User.AvatarURL = httpsURL(firstNonEmpty(m.User.MiddleIcon, m.User.SmallIcon, m.User.BigIcon))
	c.Content = m.CommentInfo
	c.Liked = m.HaveThumb == "1" || m.HaveThumb == "true"
	c.LikedCount = m.OpNumItem.ThumbNum
	c.Time = parseCommentTimeMS(m.CommentTime)
	return c
}

func parseCommentTimeMS(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		if ms < 1e12 {
			ms *= 1000
		}
		return ms
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t.UnixMilli()
	}
	return parseDateMS(s)
}

// ParseCommentThread extracts sourceID and resourceType from a "migu:<id>" or "migu:<type>:<id>" thread key.
func ParseCommentThread(thread string) (sourceID, resType string, ok bool) {
	if !strings.HasPrefix(thread, "migu:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(thread, "migu:")
	if a, b, has := strings.Cut(rest, ":"); has {
		return b, a, b != ""
	}
	return rest, "2", rest != ""
}

// ThreadComments fetches hot and paginated latest comments for a Migu song/resource.
func ThreadComments(sourceID, resType string, offset, limit int) (*netease.Comments, error) {
	if resType == "" {
		resType = "2"
	}
	if limit <= 0 {
		limit = 30
	}
	pageNo := offset/limit + 1
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
		Data struct {
			CommentNums string            `json:"commentNums"`
			Comments    []miguCommentItem `json:"comments"`
			HotComments []miguCommentItem `json:"hotComments"`
		} `json:"data"`
	}
	q := url.Values{
		"resourceId":   {sourceID},
		"resourceType": {resType},
		"pageNo":       {strconv.Itoa(pageNo)},
		"pageSize":     {strconv.Itoa(limit)},
	}
	if err := getJSON(context.Background(), appCBase+"/MIGUM3.0/user/comment/stack/v1.0?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	total := parseCommentCount(resp.Data.CommentNums)
	out := &netease.Comments{
		Total: total,
		More:  len(resp.Data.Comments) >= limit,
	}
	if pageNo == 1 {
		for _, hc := range resp.Data.HotComments {
			out.Hot = append(out.Hot, hc.toComment())
		}
	}
	for _, cm := range resp.Data.Comments {
		out.Latest = append(out.Latest, cm.toComment())
	}
	return out, nil
}

// LikeComment likes or unlikes a Migu comment (resourceType=08).
func LikeComment(commentID string, like bool) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	return collectAction(context.Background(), "08", commentID, "", "", like)
}

// AddComment posts a comment on a Migu song or playlist.
func AddComment(sourceID, resType, content string) (*netease.Comment, error) {
	uid, err := requireAuth()
	if err != nil {
		return nil, err
	}
	if resType == "" {
		resType = "2"
	}
	var resp struct {
		Code string          `json:"code"`
		Info string          `json:"info"`
		Data miguCommentItem `json:"data"`
	}
	body := map[string]string{
		"resourceId":   sourceID,
		"resourceType": resType,
		"commentInfo":  content,
		"userId":       uid,
	}
	if err := postJSON(context.Background(), appUBase+"/MIGUM3.0/user/api/comment/add/v2.0", map[string]string{"channel": defaultChn}, body, &resp); err != nil {
		return nil, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return nil, err
	}
	c := resp.Data.toComment()
	if c.Content == "" {
		c.Content = content
	}
	if c.Time == 0 {
		c.Time = time.Now().UnixMilli()
	}
	return &c, nil
}

// DeleteComment removes a comment authored by the signed-in user.
func DeleteComment(commentID string) error {
	if _, err := requireAuth(); err != nil {
		return err
	}
	var resp struct {
		Code string `json:"code"`
		Info string `json:"info"`
	}
	q := url.Values{
		"resourceId": {commentID},
		"hasAudited": {"1"},
	}
	if err := getJSON(context.Background(), appUBase+"/MIGUM2.0/v1.0/user/delComment.do?"+q.Encode(), map[string]string{"channel": defaultChn}, &resp); err != nil {
		return err
	}
	return checkCode(resp.Code, resp.Info)
}

// SongRedCount returns Migu's collection/like count for a song via queryOPNumItemsAction.do.
func SongRedCount(sourceID string) (netease.RedCount, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return netease.RedCount{}, errors.New("migu: empty song id")
	}
	var resp struct {
		Code       string `json:"code"`
		Info       string `json:"info"`
		UserOpNums []struct {
			OpNumItem struct {
				KeepNum     int    `json:"keepNum"`
				KeepNumDesc string `json:"keepNumDesc"`
			} `json:"opNumItem"`
		} `json:"userOpNums"`
	}
	u := fmt.Sprintf("%s/MIGUM2.0/v1.0/content/queryOPNumItemsAction.do?id=%s&resourceType=2&needAll=0",
		appCBase, url.QueryEscape(sourceID))
	if err := getJSON(context.Background(), u, map[string]string{"channel": defaultChn}, &resp); err != nil {
		return netease.RedCount{}, err
	}
	if err := checkCode(resp.Code, resp.Info); err != nil {
		return netease.RedCount{}, err
	}
	if len(resp.UserOpNums) == 0 {
		return netease.RedCount{}, nil
	}
	n := resp.UserOpNums[0].OpNumItem.KeepNum
	return netease.RedCount{
		Count:       n,
		Description: resp.UserOpNums[0].OpNumItem.KeepNumDesc,
	}, nil
}

// SongWikiResult carries SongWiki along with the Migu Track and Playlist models so UI navigation retains SourceIDs.
type SongWikiResult struct {
	Wiki      *netease.SongWiki
	Similar   []netease.Track
	Playlists []netease.Playlist
}

// SongWikiOf builds song wiki metadata (album, artists, facts, similar songs, playlists) from Migu APIs.
func SongWikiOf(tr netease.Track) (*SongWikiResult, error) {
	ctx := context.Background()
	w := &netease.SongWiki{}
	if tr.Album.Name != "" {
		w.Album = &netease.WikiSong{
			ID:       tr.Album.ID,
			Name:     tr.Album.Name,
			Artist:   tr.ArtistNames(),
			CoverURL: tr.Album.PicURL,
		}
	}
	for _, a := range tr.Artists {
		if a.Name != "" {
			w.Artists = append(w.Artists, netease.WikiArtist{
				ID:    a.ID,
				Name:  a.Name,
				Roles: []string{"歌手"},
			})
		}
	}
	if len(tr.Qualities) > 0 {
		w.Facts = append(w.Facts, netease.WikiFact{
			Label:  "支持音质",
			Values: tr.Qualities,
		})
	}
	res := &SongWikiResult{Wiki: w}
	// Enrich with artist intro + top similar songs when an artist source ID is available.
	for i := range w.Artists {
		sid := ""
		if i < len(tr.Artists) {
			sid = tr.Artists[i].SourceID
		}
		if sid == "" {
			continue
		}
		if ar, err := ArtistDetail(sid); err == nil && ar != nil {
			w.Artists[i].AvatarURL = firstNonEmpty(ar.Avatar, ar.PicURL)
			w.Artists[i].Brief = ar.BriefDesc
			if len(w.Similar) == 0 {
				for _, s := range ar.Songs {
					if s.ID == tr.ID {
						continue
					}
					res.Similar = append(res.Similar, s)
					w.Similar = append(w.Similar, netease.WikiSong{
						ID:       s.ID,
						Name:     s.Name,
						Artist:   s.ArtistNames(),
						CoverURL: s.Album.PicURL,
					})
					if len(w.Similar) >= 6 {
						break
					}
				}
			}
		}
	}
	// Enrich with playlists mentioning the song via Migu playlist search.
	if sr, err := SearchPageContext(ctx, tr.Name, netease.SearchPlaylists, 5, 0); err == nil {
		for _, pl := range sr.Playlists {
			res.Playlists = append(res.Playlists, pl)
			w.Playlists = append(w.Playlists, netease.WikiPlaylist{
				ID:       pl.ID,
				Name:     pl.Name,
				CoverURL: pl.Cover(),
			})
			if len(w.Playlists) >= 4 {
				break
			}
		}
	}
	return res, nil
}

// ArtistIntro fetches a Migu singer's biography as NetEase intro sections.
func ArtistIntro(sourceID string) (string, []netease.IntroSection, error) {
	ar, err := ArtistDetail(sourceID)
	if err != nil {
		return "", nil, err
	}
	return ar.BriefDesc, nil, nil
}
