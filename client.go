// Package dtekkrem looks up power outages and outage schedules for an address
// served by DTEK Kyiv Regional Electric Networks (https://www.dtek-krem.com.ua/ua/shutdowns).
//
// The site is protected by Imperva Incapsula, which rejects plain HTTP clients,
// so the package drives a headless Google Chrome (or Chromium) via the DevTools
// protocol. Chrome must be installed on the host.
package dtekkrem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	shutdownsURL   = "https://www.dtek-krem.com.ua/ua/shutdowns"
	defaultTimeout = time.Minute
	pageReadyWait  = 30 * time.Second
)

var (
	ErrUnknownCity   = errors.New("dtekkrem: city not found")
	ErrUnknownStreet = errors.New("dtekkrem: street not found")
	ErrUnknownHouse  = errors.New("dtekkrem: house not found")
	// ErrBlocked is returned when the site's bot protection rejects the request. It happens
	// when the site is queried too often (dozens of page loads within half an hour); retry later.
	ErrBlocked = errors.New("dtekkrem: request was blocked by the site's bot protection")
)

// Client looks up outages on the DTEK KREM site. The browser is started on the
// first request and kept running until Close; each request uses its own tab,
// so a Client is safe for concurrent use.
type Client struct {
	timeout    time.Duration
	allocOpts  []chromedp.ExecAllocatorOption
	mu         sync.Mutex
	browserCtx context.Context
	cancel     context.CancelFunc
}

type Option func(*Client)

// WithTimeout limits the duration of a single request (page load included). Default is one minute.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithAllocatorOptions appends chromedp allocator options, e.g. chromedp.ExecPath
// to point at a non-default Chrome binary or chromedp.Flag("headless", false) for debugging.
func WithAllocatorOptions(opts ...chromedp.ExecAllocatorOption) Option {
	return func(c *Client) { c.allocOpts = append(c.allocOpts, opts...) }
}

func NewClient(opts ...Option) *Client {
	c := &Client{
		timeout: defaultTimeout,
		allocOpts: append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", "new"),
			// Incapsula is more likely to challenge a browser that announces automation.
			chromedp.Flag("enable-automation", false),
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
		),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Close stops the browser. The Client can be reused afterwards: the next request starts a new one.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
		c.browserCtx = nil
	}
}

// Lookup returns the current outage and today's/tomorrow's schedules for the address.
// City, street and house must be spelled exactly as on the site (see Streets).
func (c *Client) Lookup(ctx context.Context, addr Address) (*Result, error) {
	var res *Result
	err := c.withPage(ctx, func(ctx context.Context) error {
		var streets []string
		if err := evalJSON(ctx, fmt.Sprintf("JSON.stringify(DisconSchedule.streets[%s] ?? null)", jsString(addr.City)), &streets); err != nil {
			return err
		}
		if streets == nil {
			return fmt.Errorf("%w: %q", ErrUnknownCity, addr.City)
		}
		if !slices.Contains(streets, addr.Street) {
			return fmt.Errorf("%w: %q in %q", ErrUnknownStreet, addr.Street, addr.City)
		}

		var resp homeNumResponse
		if err := evalJSON(ctx, fmt.Sprintf("(%s)(%s, %s)", getHomeNumJS, jsString(addr.City), jsString(addr.Street)), &resp); err != nil {
			return err
		}
		var err error
		res, err = resp.result(addr)
		return err
	})
	return res, err
}

// Streets returns all cities served by DTEK KREM, each mapped to its streets, spelled as the site expects them.
func (c *Client) Streets(ctx context.Context) (map[string][]string, error) {
	var streets map[string][]string
	err := c.withPage(ctx, func(ctx context.Context) error {
		return evalJSON(ctx, "JSON.stringify(DisconSchedule.streets)", &streets)
	})
	return streets, err
}

// getHomeNumJS repeats the request the site's own form sends (discon-schedule.js) and returns the raw JSON.
// A stale updateFact makes the server include the group schedules ("fact") and their names ("preset").
const getHomeNumJS = `async (city, street) => {
	const meta = (name) => document.querySelector('meta[name="' + name + '"]').content;
	const resp = await fetch(meta('ajaxUrl'), {
		method: 'POST',
		headers: {
			'Content-Type': 'application/x-www-form-urlencoded; charset=UTF-8',
			'X-Requested-With': 'XMLHttpRequest',
			'X-CSRF-Token': meta('csrf-token'),
		},
		body: new URLSearchParams([
			['method', 'getHomeNum'],
			['data[0][name]', 'city'], ['data[0][value]', city],
			['data[1][name]', 'street'], ['data[1][value]', street],
			['data[2][name]', 'updateFact'], ['data[2][value]', '01.01.2000 00:00'],
		]),
	});
	if (!resp.ok) throw new Error('HTTP ' + resp.status);
	return resp.text();
}`

// withPage opens the shutdowns page in a new tab, waits until its scripts are ready and runs fn in that tab.
func (c *Client) withPage(ctx context.Context, fn func(ctx context.Context) error) error {
	browserCtx, err := c.browser()
	if err != nil {
		return err
	}

	tabCtx, closeTab := chromedp.NewContext(browserCtx)
	defer closeTab()
	stop := context.AfterFunc(ctx, closeTab)
	defer stop()
	tabCtx, cancel := context.WithTimeout(tabCtx, c.timeout)
	defer cancel()

	// Don't override the user agent: that empties navigator.userAgentData, and Incapsula
	// blocks such a browser, while the stock "HeadlessChrome" one passes.
	err = chromedp.Run(tabCtx, chromedp.Navigate(shutdownsURL))
	if err == nil {
		err = chromedp.Run(tabCtx, chromedp.Poll(
			"typeof DisconSchedule !== 'undefined' && DisconSchedule.streets !== undefined",
			nil, chromedp.WithPollingTimeout(pageReadyWait),
		))
		if errors.Is(err, chromedp.ErrPollingTimeout) {
			err = ErrBlocked
		}
	}
	if err == nil {
		err = fn(tabCtx)
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// browser returns the running browser, starting it if needed.
func (c *Client) browser() (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.browserCtx != nil && c.browserCtx.Err() == nil {
		return c.browserCtx, nil
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), c.allocOpts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	cancel := func() {
		cancelBrowser()
		cancelAlloc()
	}
	if err := chromedp.Run(browserCtx); err != nil {
		cancel()
		return nil, fmt.Errorf("dtekkrem: start browser: %w", err)
	}

	c.browserCtx, c.cancel = browserCtx, cancel
	return c.browserCtx, nil
}

// evalJSON evaluates JS that returns a JSON string (or a promise of one) and decodes it into v.
func evalJSON(ctx context.Context, js string, v any) error {
	var raw string
	err := chromedp.Run(ctx, chromedp.Evaluate(js, &raw, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
	if err != nil {
		return fmt.Errorf("dtekkrem: evaluate script: %w", err)
	}
	// Incapsula answers a rate-limited AJAX request with its HTML challenge page.
	if strings.HasPrefix(strings.TrimSpace(raw), "<") {
		return ErrBlocked
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return fmt.Errorf("dtekkrem: decode response: %w", err)
	}
	return nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
