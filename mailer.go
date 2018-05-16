package main

import (
	"bytes"
	"fmt"
	"html/template"

	"github.com/prometheus/common/model"
	"github.com/spf13/viper"
    "github.com/go-mail/mail"
)

type MailRequest struct {
	from    string
	to      []string
	subject string
	body    string
}

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
    m := mail.NewMessage()
    m.SetHeader("From", viper.GetString("email"))
    m.SetHeader("To", r.to...)
    m.SetHeader("Subject", r.subject)
    m.SetBody("text/html", r.body)

    d := mail.NewDialer(viper.GetString("email_smtp"), viper.GetInt("email_port"), viper.GetString("email_username"), viper.GetString("email_password"))

    return d.DialAndSend(m)
}
