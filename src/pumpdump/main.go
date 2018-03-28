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
		apiKey            = ""
		secretKey         = ""
		asset             = flag.String("buy", "", "Asset to buy")
		tk                = flag.Float64("tk", 0.05, "Take profit after increase")
		sl                = flag.Float64("sl", 0.02, "Take profit after increase")
		total             = flag.Float64("total", 0.01, "Total in btc")
		maxPrice          = flag.Float64("maxPrice", 0, "Max price to buy")
		buyPrice          = flag.Float64("buyPrice", 0, "Price to buy. Will be ignored if race = 1")
		race              = flag.Int("race", 1, "Race to buy. If set 1, buyPrice will be ignored and race with market price. Default 1")
		delay             = flag.Int("delay", 500, "Total in btc")
		stoplossOnly      = flag.Int("stoplossOnly", 0, "If 1, only monitor and stoploss choosen asset")
		stoplossOnlyPrice = flag.Float64("stoplossOnlyPrice", 0, "stoplossOnlyPrice")
	)

	flag.Parse()

	pair := *asset

	errs := make(chan error, 2)

	binance := exchange.NewBinance(apiKey, secretKey)
	_, err := binance.GetExchangeInfo()

	// lossPrice := filledPrice - b.CalculateChangePrice(or, sl, pairInfo.PriceFilter.Tick, false)

	if err != nil {
		errs <- fmt.Errorf("Cannot get exchange info: %s", err)
	} else {
		if *stoplossOnly == 1 {
			stoplossErrs := binance.TryToStopLossForOpenOders(pair, *stoplossOnlyPrice, *delay)
			if len(stoplossErrs) > 0 {
				fmt.Printf("Finished with errors: %v", stoplossErrs)
			}
			// errs <- fmt.Errorf("Stopped protecting pair %s", pair)
		} else {
			binance.Fomo(pair, *total, *buyPrice, *maxPrice, *tk, *sl, *race, *delay, errs)
		}
	}
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	fmt.Printf("terminated: %v\n", <-errs)
}
