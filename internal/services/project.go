package services

import (
	"net/url"

	"github.com/stjudewashere/seonaut/internal/models"
	"github.com/stjudewashere/seonaut/internal/projectvalidation"
)

type (
	ArchiveRemover interface {
		DeleteArchive(*models.Project)
	}
	ProjectServiceRepository interface {
		SaveProject(*models.Project, int) error
		DeleteProject(*models.Project)
		DisableProject(*models.Project)
		UpdateProject(p *models.Project) error
		FindProjectById(id int, uid int) (models.Project, error)
		FindProjectsByUser(userId int) []models.Project

		DeleteProjectCrawls(*models.Project)
	}

	ProjectService struct {
		repository     ProjectServiceRepository
		archiveRemover ArchiveRemover
	}
)

var (
	// Error returned when the project's URL scheme is not http or https.
	ErrProtocolNotSupported = projectvalidation.ErrProtocolNotSupported

	// Error returned when the project's user agent is empty.
	ErrUserAgent = projectvalidation.ErrUserAgent
)

func NewProjectService(r ProjectServiceRepository, a ArchiveRemover) *ProjectService {
	return &ProjectService{
		repository:     r,
		archiveRemover: a,
	}
}

// SaveProject stores a new project.
// It trims the spaces in the project's URL field and checks the scheme to
// make sure it is http or https.
func (s *ProjectService) SaveProject(p *models.Project, userId int) error {
	err := projectvalidation.Prepare(p)
	if err != nil {
		return err
	}

	return s.repository.SaveProject(p, userId)
}

// Return a project specified by id and user.
// It populates the Host field from the project's URL.
func (s *ProjectService) FindProject(id, uid int) (models.Project, error) {
	project, err := s.repository.FindProjectById(id, uid)
	if err != nil {
		return project, err
	}

	parsedURL, err := url.Parse(project.URL)
	if err != nil {
		return project, err
	}

	project.Host = parsedURL.Host

	return project, nil
}

// Delete a project and its related data. Deleting the project's data can take a while
// depending on the amount of crawled URLs, so the project is marked as disabled and the
// actual data is deleted in go routine.
func (s *ProjectService) DeleteProject(p *models.Project) {
	s.repository.DisableProject(p)
	go func() {
		s.repository.DeleteProjectCrawls(p)
		s.repository.DeleteProject(p)
		s.archiveRemover.DeleteArchive(p)
	}()
}

// UpdateProject updates the project details. It first validates the project, then if the
// project's archive option is false it deletes any existing archive.
func (s *ProjectService) UpdateProject(p *models.Project) error {
	err := projectvalidation.Prepare(p)
	if err != nil {
		return err
	}

	if !p.Archive {
		s.archiveRemover.DeleteArchive(p)
	}

	return s.repository.UpdateProject(p)
}

// Delete all user projects and crawl data. This is called via a hook when users
// are deleted, so all their data is removed. The hook is added in the container
// when the service is initialized.
// It removes data from the database and WACZ archive files.
func (s *ProjectService) DeleteAllUserProjects(user *models.User) {
	projects := s.repository.FindProjectsByUser(user.Id)
	for _, p := range projects {
		s.repository.DeleteProjectCrawls(&p)
		s.repository.DeleteProject(&p)
		s.archiveRemover.DeleteArchive(&p)
	}
}
