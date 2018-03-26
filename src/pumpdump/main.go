package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"pumpdump/exchange"
	"syscall"
)

func main() {
	var (
		apiKey    = ""
		secretKey = ""
		asset     = flag.String("buy", "ETH", "Asset to buy")
		tk        = flag.Float64("tk", 0.05, "Take profit after increase")
		sl        = flag.Float64("sl", 0.02, "Take profit after increase")
		total     = flag.Float64("total", 0.01, "Total in btc")
		maxPrice  = flag.Float64("maxPrice", 0, "Max price to buy")
		delay     = flag.Int("delay", 500, "Total in btc")
	)

	flag.Parse()

	pair := *asset + "BTC"

	errs := make(chan error, 2)

	worker := exchange.NewBinance(apiKey, secretKey)
	worker.Fomo(pair, *total, *maxPrice, *tk, *sl, *delay, errs)
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	fmt.Printf("terminated: %v", <-errs)
}
