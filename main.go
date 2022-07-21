package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/broothie/v"
	"github.com/broothie/v/hx"
	"github.com/fsnotify/fsnotify"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

//go:embed static
var static embed.FS

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

func main() {
	// Flags
	port := flag.Int("p", 8888, "port to run server on")
	glob := flag.String("w", "**/**.md", "glob of files to watch")
	flag.Parse()

	// URL
	addr := fmt.Sprintf(":%d", *port)
	baseURL := fmt.Sprintf("http://localhost%s", addr)

	// File watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Println("failed to create watcher", err)
		os.Exit(1)
	}

	// Re-glob regularly
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			// Glob for filenames
			var filenames []string
			for _, glob := range strings.Fields(*glob) {
				matches, err := doublestar.FilepathGlob(glob)
				if err != nil {
					fmt.Println("failed to glob for files", err)
					os.Exit(1)
				}

				filenames = append(filenames, matches...)
			}

			// Add files to watchlist
			for _, filename := range filenames {
				if lo.Contains(watcher.WatchList(), filename) {
					continue
				}

				fmt.Printf("watching %s: %s?filename=%s\n", filename, baseURL, filename)
				if err := watcher.Add(filename); err != nil {
					fmt.Println("failed to add to watches", err)
					os.Exit(1)
				}
			}
		}
	}()

	// Process events
	eventChan := make(chan fsnotify.Event)
	go func() {
		for event := range watcher.Events {
			switch event.Op {
			case fsnotify.Write:
				eventChan <- event

			case fsnotify.Rename:
				if err := watcher.Add(event.Name); err != nil {
					fmt.Println("failed to add to watches", err)
				}

			case fsnotify.Remove:
				fmt.Printf("no longer watching %s\n", event.Name)
			}
		}
	}()

	// Process watcher errors
	go func() {
		for err := range watcher.Errors {
			fmt.Println("watcher error", err)
		}
	}()

	// Router
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	// Random Markdown fileserver
	notFound := router.NotFoundHandler()
	router.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if fmt.Sprintf("http://%s", r.Host) != baseURL {
			notFound.ServeHTTP(w, r)
			return
		}

		if r.Header.Get("Sec-Fetch-Dest") != "image" {
			notFound.ServeHTTP(w, r)
			return
		}

		http.ServeFile(w, r, strings.TrimPrefix(filepath.Clean(r.URL.Path), "/"))
	})

	// Fileserver
	router.Handle("/static/*", http.FileServer(http.FS(static)))

	// Index
	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		filename := r.URL.Query().Get("filename")

		v.Render(w, http.StatusOK, v.HTML(v.Attr{"lang": "en", "height": "100%"},
			v.Head(nil,
				v.Meta(v.Attr{"charset": "UTF-8"}),
				v.Meta(v.Attr{"name": "viewport", "content": "width=device-width, user-scalable=no, initial-scale=1.0, maximum-scale=1.0, minimum-scale=1.0"}),
				v.Meta(v.Attr{"http-equiv": "X-UA-Compatible", "content": "ie=edge"}),

				v.Stylesheet("/static/markdown.css"),
				v.JS("/static/htmx.min.js", v.Attr{"defer": true}),

				v.Title(nil,
					v.If(filename == "", v.Text("remark")),
					v.If(filename != "", v.Text(fmt.Sprintf("remark - %s", filename))),
				),
			),
			v.Body(v.Attr{
				"style": v.CSS{
					"height":      "100%",
					"margin":      0,
					"padding":     0,
					"font-family": "sans-serif",
				},
			},
				v.Div(v.Attr{
					"style": v.CSS{
						"height":    "100%",
						"display":   "flex",
						"flex-flow": "row",
					},
				},
					v.Div(v.Attr{"hx-get": "/sidebar", "hx-trigger": "load, every 1s", "style": v.CSS{"padding": "3em"}}),
					v.Div(v.Attr{
						"id": "viewer",
						"style": v.CSS{
							"height":     "100%",
							"padding":    "3em",
							"flex":       1,
							"overflow-y": "scroll",
							"box-sizing": "border-box",
						},
					},
						v.If(filename != "", hx.Frame(fmt.Sprintf("/markdown?filename=%s", filename)).WriteHTML),
					),
				),
			),
		))
	})

	// Sidebar
	router.Get("/sidebar", func(w http.ResponseWriter, r *http.Request) {
		currentFilename := ""
		if u, err := url.Parse(r.Header.Get("Hx-Current-Url")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else {
			currentFilename = u.Query().Get("filename")
		}

		watchList := watcher.WatchList()
		sort.Slice(watchList, func(i, j int) bool { return strings.ToLower(watchList[i]) < strings.ToLower(watchList[j]) })

		sort.SliceStable(watchList, func(i, j int) bool {
			return strings.ContainsRune(watchList[i], os.PathSeparator) && !strings.ContainsRune(watchList[j], os.PathSeparator)
		})

		v.Render(w, http.StatusOK, v.Div(v.Attr{
			"style": v.CSS{
				"display":   "flex",
				"flex-flow": "column",
			},
		},
			lo.Map(watchList, func(filename string, i int) v.Node {
				style := v.CSS{"padding": "0.2em"}
				if filename == currentFilename {
					style["font-weight"] = "bold"
				}

				return v.A(v.Attr{
					"href":  fmt.Sprintf("/?filename=%s", filename),
					"style": style,
				},
					v.Text(filename),
				)
			})...,
		))
	})

	// Markdown
	router.Get("/markdown", func(w http.ResponseWriter, r *http.Request) {
		filename := r.URL.Query().Get("filename")
		if !lo.Contains(watcher.WatchList(), filename) {
			hx.PageRedirect(w, r, "/", http.StatusPermanentRedirect)
			return
		}

		node, err := markdownNode(filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		v.Render(w, http.StatusOK, v.Div(v.Attr{"hx-ws": fmt.Sprintf("connect:/watch?filename=%s", filename)}, node))
	})

	// Watch
	var upgrader websocket.Upgrader
	router.Get("/watch", func(w http.ResponseWriter, r *http.Request) {
		filename := r.URL.Query().Get("filename")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		closeChan := make(chan struct{})
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					if _, isCloseError := err.(*websocket.CloseError); isCloseError {
						close(closeChan)
						return
					}
				}
			}
		}()

		for {
			select {
			case <-closeChan:
				return

			case event := <-eventChan:
				if event.Name != filename {
					continue
				}

				node, err := markdownNode(event.Name)
				if err != nil {
					fmt.Println(err)
					continue
				}

				socket, err := conn.NextWriter(websocket.TextMessage)
				if err != nil {
					if _, isCloseError := err.(*websocket.CloseError); isCloseError {
						return
					}

					fmt.Println("failed to get socket writer", err)
					continue
				}

				if _, err := v.WriteHTML(socket, node); err != nil {
					fmt.Println("failed to write html to socket", err)
				}

				socket.Close()
			}
		}
	})

	// Server
	fmt.Printf("remark running at %s\n", baseURL)
	if err := http.ListenAndServe(addr, router); err != nil {
		fmt.Println("error running server", err)
		os.Exit(1)
	}
}

func markdownNode(filename string) (v.Node, error) {
	rawMarkdown, err := os.ReadFile(filename)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read file")
	}

	outputHTML := bytes.NewBuffer([]byte{})
	if err := markdown.Convert(rawMarkdown, outputHTML); err != nil {
		return nil, errors.Wrap(err, "failed to write html to pipe")
	}

	return v.Div(v.Attr{"id": "markdown", "class": "markdown-body"}, v.FromReader(outputHTML)), nil
}
