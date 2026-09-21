package main

import (
	"go.uber.org/fx"

	"github.com/matheusj989/wagering-wallet-service/internal/app"
)

func main() {
	fx.New(app.Options()).Run()
}
