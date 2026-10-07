package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"netease2api/internal/netease"
)

type Failure struct {
	Kind      string
	Message   string
	Retryable bool
}

func (f *Failure) Error() string { return f.Message }

type Upstream interface {
	Login(context.Context, []byte) (*netease.Account, error)
	Ask(context.Context, *netease.Account, string) (string, error)
}
type FoxUpstream struct {
	Client       *netease.Client
	PollInterval time.Duration
	Skin         string
}

func (f *FoxUpstream) Login(ctx context.Context, source []byte) (*netease.Account, error) {
	account, err := f.Client.PELogin(ctx, source, "")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Only the login-stage failure can be retried on another account. No
		// credential or upstream response body is returned through the public API.
		if strings.Contains(err.Error(), "authentication rejected") || errors.Is(err, netease.ErrInvalidCookie) {
			return nil, &Failure{"login", "账号登录失败，请更新 Cookie 后重新检查", true}
		}
		return nil, &Failure{"login_network", "登录服务暂不可用，账号冷却 30 秒", true}
	}
	return account, nil
}
func (f *FoxUpstream) Ask(ctx context.Context, account *netease.Account, content string) (string, error) {
	sid, cmid := netease.NewPetIDs(time.Now().UnixMilli())
	response, err := f.Client.PetChat(ctx, account, netease.PetChatRequest{ClientMessageID: cmid, SessionID: sid, Content: content, SkinName: f.Skin})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if response != nil && response.Code != nil {
			if *response.Code == 5 {
				return "", &Failure{"quota", "账号达到对话上限，冷却 30 分钟后再尝试", true}
			}
			return "", &Failure{"rejected", "上游拒绝聊天请求，请检查账号状态", false}
		}
		// A network timeout may happen AFTER acceptance. Never duplicate this
		// question across accounts in that ambiguous state.
		return "", &Failure{"network", "发送状态不确定，请稍后检查；本次不自动重复发送", false}
	}
	interval := f.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		row, e := f.Client.FindPetAnswer(ctx, account, sid, cmid, 5)
		if e != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", &Failure{"history", "请求已接受，但历史查询失败；本次不重复发送", false}
		}
		if row != nil {
			return row.AssistantMessage, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

type Engine struct {
	Store    *Store
	Upstream Upstream
	Timeout  time.Duration
}

func (e *Engine) Complete(ctx context.Context, content, id string) (string, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	excluded := map[string]bool{}
	for attempt := 0; attempt < 8; attempt++ {
		a, err := e.Store.acquire(ctx, "", excluded)
		if err != nil {
			return "", err
		}
		start := time.Now()
		login, loginAt := a.login, a.loginAt
		if login == nil || time.Since(loginAt) > 30*time.Minute {
			login, err = e.Upstream.Login(ctx, a.Source)
			if err == nil {
				loginAt = time.Now()
			}
		}
		var answer string
		if err == nil {
			answer, err = e.Upstream.Ask(ctx, login, content)
		}
		e.Store.release(a, login, loginAt, time.Since(start), err, id, false)
		if err == nil {
			return answer, nil
		}
		var failure *Failure
		if !errors.As(err, &failure) || !failure.Retryable {
			return "", err
		}
		excluded[a.ID] = true
	}
	return "", ErrUnavailable
}
func (e *Engine) Check(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	a, err := e.Store.acquire(ctx, id, nil)
	if err != nil {
		return err
	}
	start := time.Now()
	login, err := e.Upstream.Login(ctx, a.Source)
	e.Store.release(a, login, time.Now(), time.Since(start), err, "", true)
	return err
}
