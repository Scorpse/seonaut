package repository

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stjudewashere/seonaut/internal/models"
)

func TestDeleteCrawlDataIfUnreferencedRetainsAPICrawlHistory(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM api_crawls WHERE upstream_crawl_id = ?)`)).WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"referenced"}).AddRow(true))
	(&CrawlRepository{DB: db}).DeleteCrawlDataIfUnreferenced(&models.Crawl{Id: 9})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
