// Copyright 2017 Jeff Foley. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package sources

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/fetchbot"
	"github.com/PuerkitoBio/goquery"
	"github.com/lanzay/amass/amass/core"
	"github.com/lanzay/amass/amass/utils"
)

var (
	nameStripRE = regexp.MustCompile("^((20)|(25)|(2b)|(2f)|(3d)|(3a)|(40))+")
)

// GetAllSources returns a slice of all data source services, initialized and ready.
func GetAllSources(config *core.Config, bus *core.EventBus) []core.Service {
	return []core.Service{
		NewWebsiteInformer(config, bus),
		NewAlienVault(config, bus),
		NewArchiveIt(config, bus),
		NewArchiveToday(config, bus),
		NewArquivo(config, bus),
		NewAsk(config, bus),
		NewBaidu(config, bus),
		NewBinaryEdge(config, bus),
		NewBing(config, bus),
		NewBufferOver(config, bus),
		NewCensys(config, bus),
		NewCertDB(config, bus),
		NewCertSpotter(config, bus),
		NewCIRCL(config, bus),
		NewCommonCrawl(config, bus),
		NewCrtsh(config, bus),
		NewDNSDB(config, bus),
		NewDNSDumpster(config, bus),
		NewDNSTable(config, bus),
		NewDogpile(config, bus),
		NewEntrust(config, bus),
		NewExalead(config, bus),
		NewFindSubdomains(config, bus),
		NewGoogle(config, bus),
		NewHackerOne(config, bus),
		NewHackerTarget(config, bus),
		NewIPv4Info(config, bus),
		NewLoCArchive(config, bus),
		NewMnemonic(config, bus),
		NewNetcraft(config, bus),
		NewNetworksDB(config, bus),
		NewOpenUKArchive(config, bus),
		NewPassiveTotal(config, bus),
		NewPTRArchive(config, bus),
		NewRADb(config, bus),
		NewRiddler(config, bus),
		NewRobtex(config, bus),
		NewSiteDossier(config, bus),
		NewSecurityTrails(config, bus),
		NewShadowServer(config, bus),
		NewShodan(config, bus),
		NewSublist3rAPI(config, bus),
		NewTeamCymru(config, bus),
		NewThreatCrowd(config, bus),
		NewTwitter(config, bus),
		NewUKGovArchive(config, bus),
		NewUmbrella(config, bus),
		NewURLScan(config, bus),
		NewViewDNS(config, bus),
		NewVirusTotal(config, bus),
		NewWayback(config, bus),
		NewYahoo(config, bus),
	}
}

// Clean up the names scraped from the web.
func cleanName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))

	for {
		if i := nameStripRE.FindStringIndex(name); i != nil {
			name = name[i[1]:]
		} else {
			break
		}
	}

	name = strings.Trim(name, "-")
	// Remove dots at the beginning of names
	if len(name) > 1 && name[0] == '.' {
		name = name[1:]
	}
	return name
}

//-------------------------------------------------------------------------------------------------
// Web archive crawler implementation
//-------------------------------------------------------------------------------------------------

func crawl(service core.Service, base, domain, sub string) ([]string, error) {
	var results []string
	var filterMutex sync.Mutex
	filter := make(map[string]struct{})

	year := strconv.Itoa(time.Now().Year())
	mux := fetchbot.NewMux()
	links := make(chan string, 50)
	names := make(chan string, 50)
	linksFilter := make(map[string]struct{})

	mux.HandleErrors(fetchbot.HandlerFunc(func(ctx *fetchbot.Context, res *http.Response, err error) {
		//service.Config.Log.Printf("Crawler error: %s %s - %v", ctx.Cmd.Method(), ctx.Cmd.URL(), err)
	}))

	mux.Response().Method("GET").ContentType("text/html").Handler(fetchbot.HandlerFunc(
		func(ctx *fetchbot.Context, res *http.Response, err error) {
			filterMutex.Lock()
			defer filterMutex.Unlock()

			u := res.Request.URL.String()
			if _, found := filter[u]; found {
				return
			}
			filter[u] = struct{}{}

			linksAndNames(domain, ctx, res, links, names)
		}))

	f := fetchbot.New(fetchbot.HandlerFunc(func(ctx *fetchbot.Context, res *http.Response, err error) {
		mux.Handle(ctx, res, err)
	}))
	setFetcherConfig(f)

	q := f.Start()
	u := fmt.Sprintf("%s/%s/%s", base, year, sub)
	if _, err := q.SendStringGet(u); err != nil {
		return results, fmt.Errorf("Crawler error: GET %s - %v", u, err)
	}

	t := time.NewTimer(10 * time.Second)
loop:
	for {
		select {
		case l := <-links:
			if _, ok := linksFilter[l]; ok {
				continue
			}
			linksFilter[l] = struct{}{}
			q.SendStringGet(l)
		case n := <-names:
			results = utils.UniqueAppend(results, n)
		case <-t.C:
			go func() {
				q.Cancel()
			}()
		case <-q.Done():
			break loop
		case <-service.Quit():
			break loop
		}
	}
	return results, nil
}

func linksAndNames(domain string, ctx *fetchbot.Context, res *http.Response, links, names chan string) error {
	// Process the body to find the links
	doc, err := goquery.NewDocumentFromResponse(res)
	if err != nil {
		return fmt.Errorf("crawler error: %s %s - %s", ctx.Cmd.Method(), ctx.Cmd.URL(), err)
	}

	re := utils.SubdomainRegex(domain)
	if re == nil {
		return fmt.Errorf("crawler error: Failed to obtain regex object for: %s", domain)
	}
	doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {
		val, _ := s.Attr("href")
		// Resolve address
		u, err := ctx.Cmd.URL().Parse(val)
		if err != nil {
			return
		}

		if sub := re.FindString(u.String()); sub != "" {
			names <- sub
			links <- u.String()
		}
	})
	return nil
}

func setFetcherConfig(f *fetchbot.Fetcher) {
	d := net.Dialer{}
	f.HttpClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:           d.DialContext,
			MaxIdleConns:          200,
			IdleConnTimeout:       5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 5 * time.Second,
		},
	}
	f.CrawlDelay = 1 * time.Second
	f.DisablePoliteness = true
	f.UserAgent = utils.UserAgent
}
