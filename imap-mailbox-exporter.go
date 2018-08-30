package main

import (
  "net/http"
  "sync"
  "time"
  "os"
  "strconv"
  "flag"

  "github.com/prometheus/client_golang/prometheus"
  "github.com/prometheus/common/log"
  "github.com/mxk/go-imap/imap"
)

type ImapState struct {
  nb_messages int
  unseen_messages int
  up int
}

type Exporter struct {
  mailserver string
  username string
  password string
  mailbox string
  minQueryInterval time.Duration
  lastQuery time.Time
  lastState ImapState
  mutex sync.Mutex

  up *prometheus.Desc
  nbMessages prometheus.Gauge
  unseenMessages prometheus.Gauge
}

func NewExporter(mailserver, username, password string, mailbox string, minQueryInterval time.Duration) *Exporter {
  return &Exporter{
    mailserver: mailserver,
    username: username,
    password: password,
    mailbox: mailbox,
    minQueryInterval: minQueryInterval,

    up: prometheus.NewDesc(
      prometheus.BuildFQName("imap", "", "up"),
      "Could the IMAP server be reached",
      nil,
      nil),
    nbMessages: prometheus.NewGauge(prometheus.GaugeOpts{
      Namespace: "imap",
      Name: "num_messages",
      Help: "Current number of messages in mailbox",
    }),
    unseenMessages: prometheus.NewGauge(prometheus.GaugeOpts{
      Namespace: "imap",
      Name: "unseen_messages",
      Help: "Current number of unseen messages in mailbox",
    }),

  }
}

func (exp *Exporter) Describe(ch chan<- *prometheus.Desc) {
  ch <- exp.up
  exp.nbMessages.Describe(ch)
  exp.unseenMessages.Describe(ch)
}

func (exp *Exporter) queryImapServer() ImapState {
  state := exp.lastState
  exp.lastQuery = time.Now()

  var (
    client *imap.Client
    err error
  )

  // Connect to the server
  client, err = imap.DialTLS(exp.mailserver, nil)
  if err != nil {
    state.up = 0
    return state
  }

  // Remember to log out and close the connection when finished
  defer client.Logout(30 * time.Second)

  // Enable encryption, if supported by the server
  if client.Caps["STARTTLS"] {
    client.StartTLS(nil)
  } else {
//    log.Fatal("IMAP server does not support encryption!")
  }

  // Authenticate
  if client.State() != imap.Login {
    log.Fatal("IMAP server in wrong state for Login!")
  }
  _, err = client.Login(exp.username, exp.password)
  if err != nil { log.Fatal(err) }

  // Open a mailbox read-only (synchronous command - no need for imap.Wait)
  client.Select(exp.mailbox, true)

  state.up = 1
  state.nb_messages = int(client.Mailbox.Messages)
  state.unseen_messages = int(client.Mailbox.Unseen)

  return state
}

func (exp *Exporter) collect(ch chan<- prometheus.Metric) error {

  state := exp.lastState
  if time.Since(exp.lastQuery) >= exp.minQueryInterval {
    state = exp.queryImapServer()
    exp.lastState = state
  }

  exp.nbMessages.Set(float64(state.nb_messages))
  exp.nbMessages.Collect(ch)
  exp.unseenMessages.Set(float64(state.unseen_messages))
  exp.unseenMessages.Collect(ch)

  ch <- prometheus.MustNewConstMetric(exp.up, prometheus.GaugeValue, float64(state.up))
  return nil
}

func (exp *Exporter) Collect(ch chan<- prometheus.Metric) {
  exp.mutex.Lock() // To protect metrics from concurrent collects.
  defer exp.mutex.Unlock()
  if err := exp.collect(ch); err != nil {
    log.Fatal("Scraping failure!")
  }
  return
}

var (
  imap_server = flag.String("imap.server", os.Getenv("IMAP_SERVER"), "IMAP server to query")
  imap_username = flag.String("imap.username", os.Getenv("IMAP_USERNAME"), "IMAP username for login")
  imap_password = flag.String("imap.password", os.Getenv("IMAP_PASSWORD"), "IMAP password for login")
  imap_mailbox = flag.String("imap.mailbox", os.Getenv("IMAP_MAILBOX"), "IMAP mailbox to query")
  imap_interval = flag.String("imap.query.interval", os.Getenv("IMAP_QUERY_INTERVAL"), "Minimum interval ibetween queries to IMAP server in seconds")

  listenAddress = flag.String("listen.address", os.Getenv("LISTEN_ADDRESS"), "")
  metricsEndpoint = flag.String("metrics.endpoint", os.Getenv("METRICS_ENDPOINT"), "")
)

func main() {
  flag.Parse()

  if *imap_server == "" { log.Fatal("Missing IMAP server configuration") }
  if *imap_username == "" { log.Fatal("Missing IMAP username configuration") }
  if *imap_password == "" { log.Fatal("Missing IMAP password configuration") }

  if *imap_mailbox == "" { *imap_mailbox = "INBOX" }
  if *imap_interval == "" { *imap_interval = "120" }
  if *listenAddress == "" { *listenAddress = ":9117" }
  if *metricsEndpoint == "" { *metricsEndpoint = "/metrics" }

  imap_intervali, err := strconv.Atoi(*imap_interval)
  if err != nil { log.Fatal("Invalid query interval: %s", *imap_interval) }
  imap_intervald := time.Duration(imap_intervali) * time.Second

  exporter := NewExporter(*imap_server, *imap_username, *imap_password, *imap_mailbox, imap_intervald)
  prometheus.MustRegister(exporter)

  http.Handle(*metricsEndpoint, prometheus.Handler())
  http.HandleFunc("/", func(writer http.ResponseWriter, req *http.Request) {
    writer.Write([]byte("<html><head><title>IMAP mailbox exporter</title></head><body><h1>IMAP mailbox exporter</h1></body></html>"))
  })

  log.Infof("Exporter listening on %s", *listenAddress)

  log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
