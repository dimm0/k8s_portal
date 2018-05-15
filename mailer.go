package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"html/template"
	"net/smtp"

	"github.com/prometheus/common/model"
	"github.com/spf13/viper"
)

type MailRequest struct {
	from    string
	to      []string
	subject string
	body    string
}

const (
	MIME = "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\n\n"
)

func NewMailRequest(to []string, subject string) *MailRequest {
	return &MailRequest{
		to:      to,
		subject: subject,
	}
}

func (r *MailRequest) parseTemplate(fileName string, data interface{}) error {
	t, err := template.New("gpumail.tmpl").Funcs(template.FuncMap{
		"getLabel": func(metr model.Metric, label string) string {
			return fmt.Sprintf("%s", metr[model.LabelName(label)])
		},
	}).ParseFiles(fileName)

	if err != nil {
		return err
	}

	buffer := new(bytes.Buffer)
	if err = t.ExecuteTemplate(buffer, "gpumail.tmpl", data); err != nil {
		return err
	}
	r.body = buffer.String()
	return nil

}

func (r *MailRequest) sendMail() error {
	body := "To: " + r.to[0] + "\r\nSubject: " + r.subject + "\r\n" + MIME + "\r\n" + r.body

	tlsconfig := &tls.Config{
		ServerName: viper.GetString("email_smtp"),
	}

	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:%d", viper.GetString("email_smtp"), viper.GetInt("email_port")), tlsconfig)
	if err != nil {
		return err
	}

	client, err := smtp.NewClient(conn, viper.GetString("email_smtp"))
	if err != nil {
		return err
	}

	// step 1: Use Auth
	if err = client.Auth(smtp.PlainAuth("", viper.GetString("email_username"), viper.GetString("email_password"), viper.GetString("email_smtp"))); err != nil {
		return err
	}

	// step 2: add all from and to
	if err = client.Mail(viper.GetString("email")); err != nil {
		return err
	}
	for _, k := range r.to {
		if err = client.Rcpt(k); err != nil {
			return err
		}
	}

	// Data
	w, err := client.Data()
	if err != nil {
		return err
	}

	_, err = w.Write([]byte(body))
	if err != nil {
		return err
	}

	err = w.Close()
	if err != nil {
		return err
	}

	client.Quit()
	return nil
}
