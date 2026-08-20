package services

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/stjudewashere/seonaut/internal/archiver"
	"github.com/stjudewashere/seonaut/internal/config"
	"github.com/stjudewashere/seonaut/internal/crawler"
	"github.com/stjudewashere/seonaut/internal/models"
)

const (
	CrawlLimit      = 20000 // Max number of page reports that will be created
	LastCrawlsLimit = 5     // Max number returned by GetLastCrawls
	ClientTimeout   = 10    // HTTP client timeout in seconds.
)

type CrawlerServiceRepository interface {
	SaveCrawl(models.Project) (*models.Crawl, error)
	GetLastCrawl(p *models.Project) models.Crawl
	GetLastCrawls(models.Project, int) []models.Crawl
	DeleteCrawlDataIfUnreferenced(c *models.Crawl)

	CountIssuesByPriority(int64, int) int
	UpdateCrawl(*models.Crawl) error
}

type CrawlerServicesContainer struct {
	Broker          *Broker
	ReportManager   *ReportManager
	CrawlerHandler  *CrawlerHandler
	ArchiveService  *ArchiveService
	Config          *config.CrawlerConfig
	TargetValidator crawler.TargetValidator
	Transport       http.RoundTripper
}

type CrawlerService struct {
	repository      CrawlerServiceRepository
	config          *config.CrawlerConfig
	broker          *Broker
	reportManager   *ReportManager
	crawlerHandler  *CrawlerHandler
	ArchiveService  *ArchiveService
	targetValidator crawler.TargetValidator
	transport       http.RoundTripper
	crawlers        map[int64]*crawler.Crawler
	canceled        map[int64]bool
	lock            *sync.RWMutex
}

func NewCrawlerService(r CrawlerServiceRepository, s CrawlerServicesContainer) *CrawlerService {
	return &CrawlerService{
		repository:      r,
		broker:          s.Broker,
		config:          s.Config,
		reportManager:   s.ReportManager,
		crawlerHandler:  s.CrawlerHandler,
		ArchiveService:  s.ArchiveService,
		targetValidator: s.TargetValidator,
		transport:       s.Transport,
		crawlers:        make(map[int64]*crawler.Crawler),
		canceled:        make(map[int64]bool),
		lock:            &sync.RWMutex{},
	}
}

// StartCrawler creates a new crawler and crawls the project's URL.
// It adds a new crawler for the project, it returns an error if there's one already
// running or if there's an error creating it.
// Finally the previous crawl's data is removed and the crawl is returned.
func (s *CrawlerService) StartCrawler(p models.Project, b models.BasicAuth) error {
	_, err := s.StartCrawlerObserved(p, b, "", nil)
	return err
}

// StartCrawlerObserved preserves completed crawl data for API callers while
// exposing the upstream crawl ID and one terminal callback.
func (s *CrawlerService) StartCrawlerObserved(p models.Project, b models.BasicAuth, archiveID string, completed func(*models.Crawl, bool, bool, error)) (*models.Crawl, error) {
	u, err := url.Parse(p.URL)
	if err != nil {
		return nil, err
	}

	if u.Path == "" {
		u.Path = "/"
	}

	// Acquire the in-memory lock before any DB writes so that a rejected
	// duplicate trigger cannot leave an orphaned crawl record with a NULL
	// end timestamp.
	c, err := s.addCrawler(u, &p, &b)
	if err != nil {
		return nil, err
	}

	previousCrawl := s.repository.GetLastCrawl(&p)

	crawl, err := s.repository.SaveCrawl(p)
	if err != nil {
		s.removeCrawler(&p)
		return nil, err
	}

	go func() {
		var runErr error
		archiveReady := false
		var closeArchive func() error
		defer func() {
			defer s.removeCrawler(&p)
			if recovered := recover(); recovered != nil {
				runErr = fmt.Errorf("crawler panic: %v", recovered)
				log.Printf("Crawler for %s failed: %v", p.URL, runErr)
			}
			if closeArchive != nil {
				if err := closeArchive(); err != nil {
					log.Printf("Failed to finalize archive for %s: %v", p.URL, err)
				} else {
					archiveReady = true
				}
			}
			canceled := s.crawlerCanceled(&p)
			if completed != nil {
				completed(crawl, canceled, archiveReady, runErr)
			}
		}()
		defer s.repository.DeleteCrawlDataIfUnreferenced(&previousCrawl)

		callback := s.crawlerHandler.responseCallback(crawl, &p, c)

		if p.Archive {
			var archiveWriter *archiver.Writer
			var publishArchive func() error
			var err error
			if archiveID != "" {
				archiveWriter, publishArchive, err = s.ArchiveService.GetAPIArchiveWriter(&p, archiveID)
			} else {
				archiveWriter, err = s.ArchiveService.GetArchiveWriter(&p)
			}
			if err != nil {
				log.Printf("Failed to create archive: %v", err)
			} else {
				closeArchive = func() error {
					if err := archiveWriter.Close(); err != nil {
						return err
					}
					if publishArchive != nil {
						return publishArchive()
					}
					return nil
				}
				callback = s.crawlerHandler.archiveWrapper(callback, archiveWriter)
			}
		}

		c.OnResponse(callback)

		log.Printf("Crawling %s...", p.URL)
		c.AddRequest(&crawler.RequestMessage{URL: u, Data: crawlerData{}})

		// Calling Start() initiates the website crawling process and
		// blocks execution until the crawling is complete.
		c.Start()

		crawl.RobotstxtExists = c.RobotstxtExists()
		crawl.SitemapExists = c.SitemapExists()
		crawl.SitemapIsBlocked = c.SitemapIsBlocked()
		crawl.End = time.Now()

		s.broker.Publish(fmt.Sprintf("crawl-%d", p.Id), &models.Message{Name: "IssuesInit"})
		s.reportManager.CreateMultipageIssues(crawl)

		crawl.IssuesEnd = time.Now()
		crawl.CriticalIssues = s.repository.CountIssuesByPriority(crawl.Id, Critical)
		crawl.AlertIssues = s.repository.CountIssuesByPriority(crawl.Id, Alert)
		crawl.WarningIssues = s.repository.CountIssuesByPriority(crawl.Id, Warning)
		crawl.TotalIssues = crawl.CriticalIssues + crawl.AlertIssues + crawl.WarningIssues

		runErr = s.repository.UpdateCrawl(crawl)
		if runErr != nil {
			log.Printf("Persist crawl %d: %v", crawl.Id, runErr)
		}
		s.broker.Publish(fmt.Sprintf("crawl-%d", p.Id), &models.Message{Name: "CrawlEnd", Data: crawl.TotalURLs})
		log.Printf("Crawled %d urls in %s", crawl.TotalURLs, p.URL)
	}()

	return crawl, nil
}

// Get a slice with 'LastCrawlsLimit' number of the crawls
func (s *CrawlerService) GetLastCrawls(p models.Project) []models.Crawl {
	crawls := s.repository.GetLastCrawls(p, LastCrawlsLimit)

	for len(crawls) < LastCrawlsLimit {
		crawls = append(crawls, models.Crawl{Start: time.Now()})
	}

	return crawls
}

// StopCrawler stops a crawler. If the crawler does not exsit it will just return.
func (s *CrawlerService) StopCrawler(p models.Project) {
	s.lock.Lock()
	defer s.lock.Unlock()

	crawler, ok := s.crawlers[p.Id]
	if !ok {
		return
	}

	s.canceled[p.Id] = true
	crawler.Stop()
}

// AddCrawler creates a new project crawler and adds it to the crawlers map. It returns the crawler
// on success otherwise it returns an error indicating the crawler already exists or there was an
// error creating it.
func (s *CrawlerService) addCrawler(u *url.URL, p *models.Project, b *models.BasicAuth) (*crawler.Crawler, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.crawlers[p.Id]; ok {
		return nil, errors.New("project is already being crawled")
	}

	options := &crawler.Options{
		CrawlLimit:      CrawlLimit,
		IgnoreRobotsTxt: p.IgnoreRobotsTxt,
		FollowNofollow:  p.FollowNofollow,
		IncludeNoindex:  p.IncludeNoindex,
		CrawlSitemap:    p.CrawlSitemap,
		AllowSubdomains: p.AllowSubdomains,
	}

	mainDomain := strings.TrimPrefix(u.Host, "www.")

	httpClient := &http.Client{
		Timeout:   ClientTimeout * time.Second,
		Transport: s.transport,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Make sure the user agent is not empty
	if p.UserAgent == "" {
		p.UserAgent = s.config.Agent
	}

	client := crawler.NewBasicClient(&crawler.ClientOptions{
		UserAgent:        p.UserAgent,
		BasicAuthDomains: []string{mainDomain, "www." + mainDomain},
		AuthUser:         b.AuthUser,
		AuthPass:         b.AuthPass,
		TargetValidator:  s.targetValidator,
		MaxResponseBytes: maxBodySize,
	}, httpClient)

	// Creates a new crawler with the crawler's response handler.
	s.crawlers[p.Id] = crawler.NewCrawler(u, options, client)
	s.canceled[p.Id] = false

	return s.crawlers[p.Id], nil
}

// RemoveCrawler removes a project's crawler from the crawlers map.
func (s *CrawlerService) removeCrawler(p *models.Project) {
	s.lock.Lock()
	defer s.lock.Unlock()

	delete(s.crawlers, p.Id)
	delete(s.canceled, p.Id)
}

func (s *CrawlerService) crawlerCanceled(p *models.Project) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.canceled[p.Id]
}
