// SPDX-License-Identifier: CC0-1.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	argBind     string
	argEndpoint string
	argInterval time.Duration
	argUserIds  []int
)

func init() {
	flag.StringVar(&argBind, "bind", "localhost:8080", "address and port to bind")
	flag.StringVar(&argEndpoint, "api", "http://localhost:8080", "strichliste api")

	var interval_ string
	flag.StringVar(&interval_, "interval", "5m", "interval for scraping upstream")
	flag.Parse()

	for _, idRaw := range flag.Args() {
		id, err := strconv.Atoi(idRaw)
		if err != nil {
			log.Fatalf("error: %s isn't user id\n", idRaw)
		}
		argUserIds = append(argUserIds, id)
	}

	var err error
	if argInterval, err = time.ParseDuration(interval_); err != nil {
		log.Fatal(err)
	}
}

type Strichliste struct {
	Client      http.Client
	ApiEndpoint string

	ScrapeInterval time.Duration
	ScrapeAll      bool

	UserIDs []int
	Metrics struct {
		ScrapeCycles   prometheus.Counter
		ScrapeFailures prometheus.Counter

		SystemTxCount    prometheus.Gauge
		SystemUserCount  prometheus.Gauge
		SystemBalance    prometheus.Gauge
		SystemBalanceAvg prometheus.Gauge

		UserTxCount *prometheus.GaugeVec
		UserBalance *prometheus.GaugeVec
		UserWeight  *prometheus.GaugeVec
		UserDays    *prometheus.GaugeVec
		UserDeltas  *prometheus.GaugeVec
	}
}

type Transaction struct {
	Id      int    `json:"id"`
	WhenRaw string `json:"createDate"`
	When    time.Time
	Delta   float64 `json:"value"`
	From    *string
	To      *string
	Comment *string `json:"comment"`
}

type User struct {
	Name     string         `json:"name"`
	Weight   float64        `json:"weightedCountOfPurchases"`
	Days     int            `json:"activeDays"`
	Balance  float64        `json:"balance"`
	TxCount  int            `json:"countOfTransactions"`
	TxRecent []*Transaction `json:"transactions"`
}

type System struct {
	TxCount    int     `json:"countTransactions"`
	AvgBalance float64 `json:"avgBalance"`
	UserCount  int     `json:"countUsers"`
	Balance    float64 `json:"overallBalance"`
}

func (s *Strichliste) fetchSystem() (*System, error) {
	url := fmt.Sprintf("%s/metrics", s.ApiEndpoint)

	resp, err := s.Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var system System
	if err := json.NewDecoder(resp.Body).Decode(&system); err != nil {
		return nil, err
	}
	return &system, nil
}

func parseStrichlisteTime(raw string) (*time.Time, error) {
	t, err := time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Strichliste) fetchUser(uid int) (*User, error) {
	url := fmt.Sprintf("%s/user/%d", s.ApiEndpoint, uid)

	resp, err := s.Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	fromPattern := regexp.MustCompile("^from (.*)$")
	toPattern := regexp.MustCompile("^to (.*)$")

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	for _, tx := range user.TxRecent {
		t, err := parseStrichlisteTime(tx.WhenRaw)
		if err != nil {
			return nil, err
		}
		tx.When = *t

		if tx.Comment != nil {
			if fromPattern.MatchString(*tx.Comment) {
				tx.From = &fromPattern.FindStringSubmatch(*tx.Comment)[1]
				tx.Comment = nil
				continue
			}

			if toPattern.MatchString(*tx.Comment) {
				tx.To = &toPattern.FindStringSubmatch(*tx.Comment)[1]
				tx.Comment = nil
				continue
			}
		}
	}

	return &user, nil
}

func (s *Strichliste) fetchUserList() ([]int, error) {
	url := fmt.Sprintf("%s/user", s.ApiEndpoint)

	resp, err := s.Client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var userList struct {
		Entries []struct {
			Id int `json:"id"`
		} `json:"entries"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&userList); err != nil {
		return nil, err
	}

	ids := []int{}
	for _, user := range userList.Entries {
		ids = append(ids, user.Id)
	}
	return ids, nil
}

func every(interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	fn()
	for {
		select {
		case <-ticker.C:
			fn()
		}
	}
}

func (s *Strichliste) scrape() {
	s.Metrics.ScrapeCycles.Inc()

	metrics, err := s.fetchSystem()
	if err != nil {
		s.Metrics.ScrapeFailures.Inc()
		log.Println("error: could not fetch system metrics:", err)
	} else {
		s.updateSystemMetrics(metrics)
	}

	if s.ScrapeAll {
		var err error
		if s.UserIDs, err = s.fetchUserList(); err != nil {
			s.Metrics.ScrapeFailures.Inc()
			log.Println("error: could not fetch user list:", err)
			return
		}
	}

	for _, uid := range s.UserIDs {
		user, err := s.fetchUser(uid)
		if err != nil {
			s.Metrics.ScrapeFailures.Inc()
			log.Println("error: could not fetch user:", uid, err)
			continue
		}
		s.updateMetricsForUser(user)
	}
}

func mkCounter(name, help string, labels ...string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "strichliste",
		Name:      name,
		Help:      help,
	})
}

func mkGauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "strichliste",
		Name:      name,
		Help:      help,
	})
}

func mkGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "strichliste",
		Name:      name,
		Help:      help,
	}, labels)
}

func (s *Strichliste) updateSystemMetrics(system *System) {
	s.Metrics.SystemTxCount.Set(float64(system.TxCount))
	s.Metrics.SystemUserCount.Set(float64(system.UserCount))
	s.Metrics.SystemBalance.Set(system.Balance)
	s.Metrics.SystemBalanceAvg.Set(system.AvgBalance)
}

func (s *Strichliste) updateMetricsForUser(user *User) {
	s.Metrics.UserTxCount.WithLabelValues(user.Name).Set(float64(user.TxCount))
	s.Metrics.UserBalance.WithLabelValues(user.Name).Set(user.Balance)
	s.Metrics.UserWeight.WithLabelValues(user.Name).Set(user.Weight)
	s.Metrics.UserDays.WithLabelValues(user.Name).Set(float64(user.Days))

	s.Metrics.UserDeltas.Reset()
	for _, tx := range user.TxRecent {
		if tx.When.Add(s.ScrapeInterval).After(time.Now()) {
			continue
		}

		from := ""
		if tx.From != nil {
			from = *tx.From
		}

		to := ""
		if tx.To != nil {
			to = *tx.To
		}

		s.Metrics.UserDeltas.WithLabelValues(
			user.Name,
			strconv.Itoa(tx.Id),
			from,
			to,
		).Set(tx.Delta)
	}
}

func (s *Strichliste) initMetrics(registry *prometheus.Registry) {

	s.Metrics.ScrapeCycles = mkCounter("scrape_cycles", "number of scrape cycles")
	s.Metrics.ScrapeFailures = mkCounter("scrape_failures", "number of failed scrape cycles")

	s.Metrics.SystemTxCount = mkGauge("system_tx_count", "total number of TXs")
	s.Metrics.SystemUserCount = mkGauge("users", "total user count")
	s.Metrics.SystemBalance = mkGauge("system_balance", "total system balance")
	s.Metrics.SystemBalanceAvg = mkGauge("balance_avg", "average user balance")
	s.Metrics.UserTxCount = mkGaugeVec("tx_count", "total number of user TXs", "user")
	s.Metrics.UserBalance = mkGaugeVec("balance", "account balance", "user")
	s.Metrics.UserWeight = mkGaugeVec("weight", "account weight", "user")
	s.Metrics.UserDays = mkGaugeVec("days", "total number of days with activity", "user")
	s.Metrics.UserDeltas = mkGaugeVec("tx", "transaction", "user", "id", "from", "to")

	registry.MustRegister(s.Metrics.ScrapeCycles)
	registry.MustRegister(s.Metrics.ScrapeFailures)
	registry.MustRegister(s.Metrics.SystemTxCount)
	registry.MustRegister(s.Metrics.SystemUserCount)
	registry.MustRegister(s.Metrics.SystemBalance)
	registry.MustRegister(s.Metrics.SystemBalanceAvg)
	registry.MustRegister(s.Metrics.UserTxCount)
	registry.MustRegister(s.Metrics.UserBalance)
	registry.MustRegister(s.Metrics.UserWeight)
	registry.MustRegister(s.Metrics.UserDays)
	registry.MustRegister(s.Metrics.UserDeltas)
}

func main() {

	s := Strichliste{
		ApiEndpoint:    argEndpoint,
		ScrapeInterval: argInterval,
		ScrapeAll:      len(argUserIds) == 0,
		UserIDs:        argUserIds,
	}

	registry := prometheus.NewRegistry()
	s.initMetrics(registry)

	go every(s.ScrapeInterval, s.scrape)

	http.Handle("/metrics", promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
			Registry:          registry,
		},
	))

	log.Fatal(http.ListenAndServe(argBind, nil))
}
