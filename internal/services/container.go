package services

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/config"
	"github.com/stjudewashere/seonaut/internal/issues/multipage"
	"github.com/stjudewashere/seonaut/internal/issues/page"
	"github.com/stjudewashere/seonaut/internal/repository"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

var ErrDatabaseUnavailable = errors.New("database unavailable")

type Container struct {
	Config             *config.Config
	PubSubBroker       *Broker
	IssueService       *IssueService
	ReportService      *ReportService
	ReportManager      *ReportManager
	UserService        *UserService
	DashboardService   *DashboardService
	ProjectService     *ProjectService
	ProjectViewService *ProjectViewService
	ExportService      *Exporter
	CrawlerService     *CrawlerService
	Translator         *Translator
	Renderer           *Renderer
	CookieSession      *CookieSession
	ArchiveService     *ArchiveService
	ReplayService      *ReplayService
	APIAuthenticator   api.Authenticator
	APIKeyManager      api.KeyManager
	APITenantManager   api.TenantManager
	APIProjectManager  api.ProjectManager
	APICrawlManager    api.CrawlManager
	APIFindings        api.FindingService
	APIExports         *APIExportManager
	APIRateLimiter     *api.FixedWindowLimiter
	APICrawlSlots      *api.ConcurrencyBudget
	APIExportSlots     *api.ConcurrencyBudget
	APITargetPolicy    *api.TargetPolicy
	APIAudit           api.AuditSink

	db                   *sql.DB
	issueRepository      *repository.IssueRepository
	pageReportRepository *repository.PageReportRepository
	userRepository       *repository.UserRepository
	projectRepository    *repository.ProjectRepository
	exportRepository     *repository.ExportRepository
	crawlRepository      *repository.CrawlRepository
	dashboardRepository  *repository.DashboardRepository
	apiKeyRepository     *repository.APIKeyRepository
	apiTenantRepository  *repository.APITenantRepository
	apiProjectRepository *repository.APIProjectRepository
	apiCrawlRepository   *repository.APICrawlRepository
	apiFindingRepository *repository.APIFindingRepository
	apiExportRepository  *repository.APIExportRepository
}

func (c *Container) Ready(ctx context.Context) error {
	if c.db == nil {
		return ErrDatabaseUnavailable
	}
	return c.db.PingContext(ctx)
}

func NewContainer(configFile string) *Container {
	c := &Container{}
	c.InitConfig(configFile)
	c.InitDB()
	c.InitArchiveService()
	c.InitRepositories()
	c.InitAPIServices()
	c.InitPubSubBroker()
	c.InitIssueService()
	c.InitReportService()
	c.InitReportManager()
	c.InitTranslator()
	c.InitUserService()
	c.InitDashboardService()
	c.InitProjectService()
	c.InitProjectViewService()
	c.InitExportService()
	c.InitAPIExportService()
	c.InitCrawlerService()
	c.InitRenderer()
	c.InitCookieSession()
	c.InitReplayService()

	return c
}

// Load config file using the parameters in configFile.
func (c *Container) InitConfig(configFile string) {
	config, err := config.NewConfig(configFile)
	if err != nil {
		log.Fatalf("Error loading config: %v\n", err)
	}

	c.Config = config
}

// Create the sql database connection and run migrations.
func (c *Container) InitDB() {
	db, err := repository.SqlConnect(c.Config.DB)
	if err != nil {
		log.Fatalf("Error creating new database connection: %v", err)
	}

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		log.Fatalf("Error creating mysql driver: %v", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://migrations", "mysql", driver)
	if err != nil {
		log.Fatalf("Error with mysql migrations: %v", err)
	}

	m.Up()

	c.db = db
}

// Create the data repositories.
func (c *Container) InitRepositories() {
	c.issueRepository = &repository.IssueRepository{DB: c.db}
	c.pageReportRepository = &repository.PageReportRepository{DB: c.db}
	c.userRepository = &repository.UserRepository{DB: c.db}
	c.projectRepository = &repository.ProjectRepository{DB: c.db}
	c.exportRepository = &repository.ExportRepository{DB: c.db}
	c.crawlRepository = &repository.CrawlRepository{DB: c.db}
	c.dashboardRepository = &repository.DashboardRepository{DB: c.db}
	c.apiKeyRepository = &repository.APIKeyRepository{DB: c.db}
	c.apiTenantRepository = &repository.APITenantRepository{DB: c.db}
	c.apiProjectRepository = &repository.APIProjectRepository{DB: c.db}
	c.apiCrawlRepository = &repository.APICrawlRepository{DB: c.db}
	c.apiFindingRepository = &repository.APIFindingRepository{DB: c.db}
	c.apiExportRepository = &repository.APIExportRepository{DB: c.db}
	c.APIAudit = &repository.APIAuditRepository{DB: c.db}

	if _, err := c.apiCrawlRepository.RecoverInterruptedCrawls(context.Background(), time.Now().UTC()); err != nil {
		log.Printf("Recover interrupted API crawls: %v", err)
	}
	// Clean up unfinished UI crawls. API crawl rows and partial findings survive
	// restart recovery for machine inspection.
	c.crawlRepository.DeleteUnfinishedCrawls()
}

func (c *Container) InitAPIServices() {
	var err error
	c.APITargetPolicy, err = api.NewTargetPolicy(c.Config.API.Environment, c.Config.API.FixtureHosts, nil)
	if err != nil {
		log.Fatalf("Configure API target policy: %v", err)
	}
	c.APIRateLimiter = api.NewFixedWindowLimiter(time.Minute, map[api.RateClass]int{
		api.RateRead: 600, api.RateCrawl: 30, api.RateExport: 120,
	}, nil)
	c.APICrawlSlots = api.NewConcurrencyBudget(2, 8)
	c.APIExportSlots = api.NewConcurrencyBudget(4, 16)
	c.APIAuthenticator, c.APIKeyManager = NewAPIServices(c.Config.API, c.apiKeyRepository)
	c.APITenantManager = api.TenantManager{Store: c.apiTenantRepository, Keys: c.APIKeyManager}
	c.APIProjectManager = api.ProjectManager{Store: c.apiProjectRepository, Targets: c.APITargetPolicy}
	c.APICrawlManager = api.CrawlManager{Store: c.apiCrawlRepository, Projects: c.apiProjectRepository, Slots: c.APICrawlSlots, Targets: c.APITargetPolicy}
	c.APIFindings = c.apiFindingRepository
}

// Create the PubSub broker.
func (c *Container) InitPubSubBroker() {
	c.PubSubBroker = NewPubSubBroker()
}

// Create the issue service.
func (c *Container) InitIssueService() {
	c.IssueService = NewIssueService(c.issueRepository)
}

// Create the report service.
func (c *Container) InitReportService() {
	repository := &struct {
		*repository.PageReportRepository
		*repository.IssueRepository
	}{
		c.pageReportRepository,
		c.issueRepository,
	}

	c.ReportService = NewReportService(repository)
}

// Create the report manager and add all the available reporters.
func (c *Container) InitReportManager() {
	c.ReportManager = NewReportManager(c.issueRepository)
	for _, r := range page.GetAllReporters() {
		c.ReportManager.AddPageReporter(r)
	}

	// Create the sql multipage reporters and add them all to the reporterManager.
	sqlReporters := multipage.NewSqlReporter(c.db)
	for _, r := range sqlReporters.GetAllReporters() {
		c.ReportManager.AddMultipageReporter(r)
	}
}

// Create the user service.
func (c *Container) InitUserService() {
	c.UserService = NewUserService(c.userRepository, c.Translator)
}

// Create the Project service.
func (c *Container) InitProjectService() {
	repository := &struct {
		*repository.ProjectRepository
		*repository.CrawlRepository
	}{
		c.projectRepository,
		c.crawlRepository,
	}

	c.ProjectService = NewProjectService(repository, c.ArchiveService)

	// UserService DeleteHooks are called when a user is deleted.
	// Add a DeleteHook so it deletes all user projects and crawl
	// data when a user is deleted.
	c.UserService.AddDeleteHook(c.ProjectService.DeleteAllUserProjects)
}

// Create the ProjectView service.
func (c *Container) InitProjectViewService() {
	repository := &struct {
		*repository.ProjectRepository
		*repository.CrawlRepository
	}{
		c.projectRepository,
		c.crawlRepository,
	}

	c.ProjectViewService = NewProjectViewService(repository)
}

// Create the Export service.
func (c *Container) InitExportService() {
	c.ExportService = NewExporter(c.exportRepository, c.Translator)
}

func (c *Container) InitAPIExportService() {
	c.APIExports = &APIExportManager{
		Store:    c.apiExportRepository,
		Findings: c.APIFindings,
		Exporter: c.ExportService,
		Archives: c.ArchiveService,
	}
	c.APICrawlManager.CompletionObserver = c.APIExports
	if err := c.APIExports.PurgeExpiredArchives(context.Background(), time.Now().UTC()); err != nil {
		log.Printf("Purge expired API archives: %v", err)
	}
}

// Create Crawler service.
func (c *Container) InitCrawlerService() {
	crawlerServices := CrawlerServicesContainer{
		Broker:          c.PubSubBroker,
		ReportManager:   c.ReportManager,
		CrawlerHandler:  NewCrawlerHandler(c.pageReportRepository, c.PubSubBroker, c.ReportManager),
		ArchiveService:  c.ArchiveService,
		Config:          c.Config.Crawler,
		TargetValidator: c.APITargetPolicy,
		Transport:       c.APITargetPolicy.Transport(),
	}
	repository := &struct {
		*repository.CrawlRepository
		*repository.IssueRepository
	}{
		c.crawlRepository,
		c.issueRepository,
	}

	c.CrawlerService = NewCrawlerService(repository, crawlerServices)
	c.APICrawlManager.Runner = c.CrawlerService
}

// Create the dashboCallbackBuilderard service.
func (c *Container) InitDashboardService() {
	c.DashboardService = NewDashboardService(c.dashboardRepository)
}

// Create The translator.
func (c *Container) InitTranslator() {
	var err error
	c.Translator, err = NewTranslator("translations", c.Config.UIConfig.Language)
	if err != nil {
		log.Fatal(err)
	}
}

// Create html renderer.
func (c *Container) InitRenderer() {
	renderer, err := NewRenderer(&RendererConfig{
		TemplatesFolder: "web/templates",
	}, c.Translator)
	if err != nil {
		log.Fatal(err)
	}

	c.Renderer = renderer
}

// Create cookie session handler
func (c *Container) InitCookieSession() {
	c.CookieSession = NewCookieSession(c.userRepository)
}

// Init the WACZ archiver service.
func (c *Container) InitArchiveService() {
	c.ArchiveService = NewArchiveService("archive")
}

// Init the WACZ archive replay service.
func (c *Container) InitReplayService() {
	c.ReplayService = NewReplayService()
}
