package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/ner"
	"github.com/bornholm/indecis"
	"github.com/bornholm/indecis-ner/recognizer"
)

// The masking model of indecis-ner, attached to its GitHub releases: it finds
// direct and quasi-identifiers ("mask" question) and the special categories of
// GDPR articles 9 and 10 ("sensitive"), and types each mask ("entities").
const (
	modelName    = "work-fam-p576"
	modelRelease = "v0.2.0"
	// defaultModelURL is where the files of modelFiles are downloaded from.
	defaultModelURL = "https://github.com/Bornholm/indecis-ner/releases/download/" + modelRelease + "/"
)

// modelQuestions are the span questions whose passages are masked, as
// indecis-caviardage does: their union is refined and typed by the recognizer.
var modelQuestions = []string{"mask", "sensitive"}

// modelFile is a file of the model release, with the size and digest it is
// checked against once downloaded.
type modelFile struct {
	name   string
	size   int64
	sha256 string
}

var modelFiles = []modelFile{
	{"config.json", 475, "e8a7b45c0a48e6e1b5994b901726123591010dff5b9c115b238127dbaee85f7e"},
	{"indecis.json", 1089, "73b9721ec4732605686fff7a61aad599b0aef2e1e0f6e2edf672085c456284f2"},
	{"model.safetensors", 276486560, "042d04a43a1acac4f2dd6db32447a4bb38d9b1303a9ce90cf50a616e446b80a4"},
	{"tokenizer.json", 34363442, "8bd47075711f75a143d1b78e01a41cc65c1c591b00d3cfeffc23db07adce1392"},
}

// maxDecisions bounds the texts the model reads at a time, all requests
// together. Each reading already spreads over every core and holds the
// encoder buffers (about 60 MB): more would add memory, not throughput.
const maxDecisions = 4

// maxDecisionWait bounds the wait for a reading slot: past it, the reading
// fails with errModelBusy rather than hold the request until its client
// gives up.
const maxDecisionWait = 30 * time.Second

// errModelBusy reports a text the model could not read in time, every slot
// being taken by other readings.
var errModelBusy = fmt.Errorf("le modèle indecis est occupé : aucune des %d places de lecture ne s'est libérée en %s", maxDecisions, maxDecisionWait)

// errModelMissing reports a model that offline mode forbids downloading.
var errModelMissing = errors.New("le modèle n'est pas en cache et le mode hors-ligne interdit son téléchargement")

// modelSource is where a model comes from: a directory given by the operator,
// used as is, or the cache, filled from url when a file is missing.
type modelSource struct {
	dir     string
	cache   string
	url     string
	offline bool
}

func modelSourceFromConfig(cfg Config) (modelSource, error) {
	if cfg.ModelDir != "" {
		return modelSource{dir: cfg.ModelDir}, nil
	}
	root := cfg.CacheDir
	if root == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return modelSource{}, fmt.Errorf("cache directory: %w", err)
		}
		root = filepath.Join(userCache, "indecis-ner")
	}
	url := cfg.ModelURL
	if url == "" {
		url = defaultModelURL
	}
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return modelSource{
		cache:   filepath.Join(root, modelName+"-"+modelRelease),
		url:     url,
		offline: cfg.Offline,
	}, nil
}

// path returns the directory the model is loaded from.
func (s modelSource) path() string {
	if s.dir != "" {
		return s.dir
	}
	return s.cache
}

// cached reports whether every file of the release is in the cache, at its
// size: a cheap check, for the status of the configuration page. Loading
// checks each file by its digest (intact), and downloads again one that was
// altered. A directory given by the operator is always taken as it is.
func (s modelSource) cached() bool {
	if s.dir != "" {
		return true
	}
	for _, f := range modelFiles {
		if st, err := os.Stat(filepath.Join(s.cache, f.name)); err != nil || st.Size() != f.size {
			return false
		}
	}
	return true
}

// fetch downloads into the cache the files that are missing or altered. A
// cached file is checked against its digest, not only its size: a file of the
// right size but other bytes would otherwise fail every load, the cache never
// repaired.
func (s modelSource) fetch(ctx context.Context) error {
	if s.dir != "" {
		return nil
	}
	if err := os.MkdirAll(s.cache, 0o755); err != nil {
		return fmt.Errorf("create model cache: %w", err)
	}
	for _, f := range modelFiles {
		if intact(filepath.Join(s.cache, f.name), f) {
			continue
		}
		if s.offline {
			return errModelMissing
		}
		if err := s.download(ctx, f); err != nil {
			return fmt.Errorf("download %s: %w", f.name, err)
		}
	}
	return nil
}

// intact reports whether path holds f, by its size and digest.
func intact(path string, f modelFile) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	if st, err := file.Stat(); err != nil || st.Size() != f.size {
		return false
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return false
	}
	return hex.EncodeToString(digest.Sum(nil)) == f.sha256
}

// downloadClient gives up on a server that does not answer, and
// downloadTimeout on a download that stalls: the load outlives the request
// that started it, and the requests that wait for it must see it fail.
var downloadClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}()

const downloadTimeout = 30 * time.Minute

// download writes f next to its final path, checks its size and digest, then
// renames it: an interrupted or altered download never reaches the cache.
func (s modelSource) download(ctx context.Context, f modelFile) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+f.name, nil)
	if err != nil {
		return err
	}
	res, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %s", res.Status)
	}
	tmp, err := os.CreateTemp(s.cache, f.name+".*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, digest), io.LimitReader(res.Body, f.size+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != f.size {
		return fmt.Errorf("size %d, want %d", n, f.size)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != f.sha256 {
		return fmt.Errorf("sha256 %s, want %s", got, f.sha256)
	}
	return os.Rename(tmp.Name(), filepath.Join(s.cache, f.name))
}

// models loads each model once per directory and bias. The bias is set on the
// model and must not change during a decision, so each one has its instance.
// Instances are kept for the life of the process: the keys are the few
// directories and biases the nodes are configured with, and an instance of a
// file already loaded costs its int8 encoder layers only, indecis mapping the
// weights shared and loading the tokenizer once per content.
type models struct {
	mu      sync.Mutex
	loading map[string]*modelLoad
	// decisions is shared by every instance: see maxDecisions.
	decisions chan struct{}
}

type modelLoad struct {
	done  chan struct{}
	model *indecis.Model
	err   error
}

// get returns the model of src at bias, downloading and loading it on the
// first call, and the decision slots its readings share with the others. The
// download goes on when ctx ends, so that the next request finds it done; a
// failed load is tried again by the next call.
func (m *models) get(ctx context.Context, src modelSource, bias float64) (*indecis.Model, chan struct{}, error) {
	key := src.path() + "\x00" + strconv.FormatFloat(bias, 'g', -1, 64)
	m.mu.Lock()
	if m.loading == nil {
		m.loading = map[string]*modelLoad{}
		m.decisions = make(chan struct{}, maxDecisions)
	}
	decisions := m.decisions
	load, ok := m.loading[key]
	if !ok {
		load = &modelLoad{done: make(chan struct{})}
		m.loading[key] = load
		go func() {
			defer close(load.done)
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), downloadTimeout)
			defer cancel()
			load.model, load.err = loadModel(ctx, src, bias)
			if load.err != nil {
				m.mu.Lock()
				delete(m.loading, key)
				m.mu.Unlock()
			}
		}()
	}
	m.mu.Unlock()
	select {
	case <-load.done:
		return load.model, decisions, load.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func loadModel(ctx context.Context, src modelSource, bias float64) (*indecis.Model, error) {
	if err := src.fetch(ctx); err != nil {
		return nil, err
	}
	model, err := indecis.Load(src.path(), indecis.WithInt8(),
		indecis.WithSpanBias("mask", bias), indecis.WithSpanBias("sensitive", bias))
	if err != nil {
		return nil, fmt.Errorf("load model %s: %w", src.path(), err)
	}
	return model, nil
}

// gatedRecognizer reads a text with the model once a decision slot is free.
// The wait for a slot ends with the request, or with errModelBusy after wait,
// and the reading itself stops with the request.
type gatedRecognizer struct {
	rec       *recognizer.Recognizer
	decisions chan struct{}
	wait      time.Duration
}

var _ goanon.Recognizer = gatedRecognizer{}

func (g gatedRecognizer) Recognize(ctx context.Context, text string) ([]ner.Entity, error) {
	timer := time.NewTimer(g.wait)
	defer timer.Stop()
	select {
	case g.decisions <- struct{}{}:
	case <-timer.C:
		return nil, errModelBusy
	case <-ctx.Done():
		return nil, fmt.Errorf("attente d'une place de lecture du modèle indecis : %w", ctx.Err())
	}
	defer func() { <-g.decisions }()
	return g.rec.Recognize(ctx, text)
}
