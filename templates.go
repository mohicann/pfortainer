package main

import (
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates/*
var templateFS embed.FS

func render(w http.ResponseWriter, page string, data any) {
	var (
		tmpl     *template.Template
		rootName string
		err      error
	)
	if page == "login" {
		tmpl, err = template.ParseFS(templateFS, "templates/login.html")
		rootName = "login"
	} else {
		tmpl, err = template.ParseFS(templateFS, "templates/base.html", "templates/"+page+".html")
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
	http.Error(w, "Podman 연결 실패: "+err.Error(), http.StatusServiceUnavailable)
}
