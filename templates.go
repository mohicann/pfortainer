package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed templates/*
var templateFS embed.FS

var tmplFuncs = template.FuncMap{
	"dir": filepath.Dir,
}

func render(w http.ResponseWriter, page string, data any) {
	var (
		tmpl     *template.Template
		rootName string
		err      error
	)
	if page == "login" {
		tmpl, err = template.New("login").Funcs(tmplFuncs).ParseFS(templateFS, "templates/login.html")
		rootName = "login"
	} else {
		tmpl, err = template.New("base").Funcs(tmplFuncs).ParseFS(templateFS, "templates/base.html", "templates/"+page+".html")
		rootName = "base"
	}
	if err != nil {
		log.Printf("template parse error (%s): %v", page, err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, rootName, data); err != nil {
		log.Printf("template execute error (%s): %v", page, err)
	}
}

func renderErr(w http.ResponseWriter, err error) {
	isPodmanUnavailable := isSocketError(err)
	status := http.StatusServiceUnavailable
	if !isPodmanUnavailable {
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	render(w, "error", map[string]any{
		"ActivePage":         "",
		"PodmanUnavailable":  isPodmanUnavailable,
		"Error":              err.Error(),
	})
}

func isSocketError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connect: no such file or directory")
}
