package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	binance "github.com/anhpha/go-binance"
)

func main() {
	var (
		apiKey    = ""
		secretKey = ""
		asset     = flag.String("buy", "ETH", "Asset to buy")
		tk        = flag.Float64("tk", 0.05, "Take profit after increase")
		sl        = flag.Float64("sl", 0.02, "Take profit after increase")
		total     = flag.Float64("total", 0.01, "Total in btc")
		delay     = flag.Int64("delay", 500, "Total in btc")
	)

	flag.Parse()

	pair := *asset + "BTC"
	// total := 0.01
	// tk := 0.05
	// sl := 0.02

	client := binance.NewClient(apiKey, secretKey)

	willBuyPrice, err := getLastPrice(client, pair)
	if err != nil {
		fmt.Printf("Cannot get last price: %v", err)
		panic("Stopped with error.")
	}
	fmt.Printf("Last price of %s is %s: ", pair, willBuyPrice)

	price, err := strconv.ParseFloat(willBuyPrice, 64)
	if err != nil {
		fmt.Printf("Invalid price format: %v\n", err)
		panic("Stopped with error.")
	}
	willBuyAmount := int(*total / price)
	fmt.Printf("Will buy: %v\n", willBuyAmount)
	fmt.Printf("Will buy to toal in btc: %v\n", total)

	order, err := tryMarketOrderUltilSuccess(client, pair, intToString(willBuyAmount), 10)
	if err != nil {
		fmt.Println(err)
		panic("Stopped with error.")
	}
	fmt.Printf("Ordered: %v\n", order)
	errs := make(chan error, 2)
	// try for 10 times
	if err == nil {
		tkPrice := price * (1 + *tk)
		go tryTakeProfitUltilSuccess(errs, client, pair,
			intToString(willBuyAmount), tkPrice, *delay)
		slPrice := price * (1 - *sl)
		go tryStoplossUltilSuccess(errs, client, pair,
			intToString(willBuyAmount), slPrice, *delay)
	}
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT)
		errs <- fmt.Errorf("%s", <-c)
	}()

	fmt.Printf("terminated: %v", <-errs)
}

func tryTakeProfitUltilSuccess(stopChanel chan error, client *binance.Client, pair string, quantity string, tkPrice float64, delay int64) (*binance.CreateOrderResponse, error) {
	fmt.Print("Start checking profit\n")
	ok := 0
	tries := 1
	var err error
	for ok != 1 {
		lastPrice, err := getLastPrice(client, pair)
		price, err := strconv.ParseFloat(lastPrice, 64)
		fmt.Printf("Checking price to take profit: %s\n", lastPrice)
		// return if success
		if err != nil && price >= tkPrice {
			order, orderErr := createMarketOrder(client, pair, quantity)
			if orderErr != nil {
				ok = 1
				fmt.Printf("Taken profit @ %v\n", order)
				stopChanel <- fmt.Errorf("%s", "Taken profit")
				return order, nil
			}
		}
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tries++
	}

	return nil, err
}
func tryStoplossUltilSuccess(stopChanel chan error, client *binance.Client, pair string, quantity string, slPrice float64, delay int64) (*binance.CreateOrderResponse, error) {
	fmt.Print("Start checking loss\n")
	ok := 0
	tries := 1
	var err error
	for ok != 1 {
		lastPrice, err := getLastPrice(client, pair)
		price, err := strconv.ParseFloat(lastPrice, 64)
		fmt.Printf("Checking price to stop loss: %s\n", lastPrice)
		// return if success
		if err != nil && price <= slPrice {
			order, orderErr := createMarketOrder(client, pair, quantity)
			if orderErr != nil {
				ok = 1
				fmt.Printf("Stop loss @ %v\n", order)
				stopChanel <- fmt.Errorf("%s", "Stop loss")
				return order, nil
			}
		}
		// try again after 0.2 s
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tries++
	}

	return nil, err
}

func tryMarketOrderUltilSuccess(client *binance.Client, pair string, quantity string, maxTry int) (*binance.CreateOrderResponse, error) {
	ok := 0
	tries := 1
	var err error
	// order, err := createMarketOrder(client, pair, quantity)
	for ok != 1 && tries < maxTry {
		order, err := createMarketOrder(client, pair, quantity)
		// return if success
		if err == nil {
			ok = 1
			return order, nil
		}
		// try again after 0.2 s
		time.Sleep(200 * time.Millisecond)
		tries++
	}

	return nil, err
}

func trySellLimitOrderUltilSuccess(client *binance.Client, orderType binance.OrderType, pair string, price string, quantity string, maxTry int) (*binance.CreateOrderResponse, error) {
	ok := 0
	tries := 1
	order, err := createSellLimitOrder(client, orderType, pair,
		price, quantity)
	for ok != 1 && tries < maxTry {
		order, err = createSellLimitOrder(client, orderType, pair,
			price, quantity)
		// return if success
		if err == nil {
			ok = 1
			fmt.Printf("Success fully put %s with price %s @ %s\n", orderType, price, time.Now().String())
			return order, nil
		}
		fmt.Printf("Fail to put %s order at try %d, error: %v\n", orderType, tries, err)
		// try again after 0.2 s
		time.Sleep(200 * time.Millisecond)
		tries++
	}
	fmt.Printf("Cannot put order type of %s\n", orderType)
	return nil, err
}

func floatToString(input float64) string {
	// to convert a float number to a string
	return strconv.FormatFloat(input, 'f', 6, 64)
}

func intToString(input int) string {
	// to convert a float number to a string
	return strconv.Itoa(input)
}

func createSellLimitOrder(client *binance.Client, orderType binance.OrderType, pair string, price string, quantity string) (*binance.CreateOrderResponse, error) {
	s := client.NewCreateOrderService().Symbol(pair).
		Side(binance.SideTypeSell).Type(orderType).Quantity(quantity).StopPrice(price)

	if orderType == "STOP_LOSS_LIMIT" || orderType == "TAKE_PROFIT_LIMIT" || orderType == "LIMIT" {
		s = s.TimeInForce(binance.TimeInForceGTC).Price(price)
	}
	return s.Do(context.Background())
}

func createMarketOrder(client *binance.Client, pair string, quantity string) (*binance.CreateOrderResponse, error) {
	return client.NewCreateOrderService().Symbol(pair).
		Side(binance.SideTypeBuy).Type(binance.OrderTypeMarket).Quantity(quantity).Do(context.Background())
}

// Get last price of a pair
func getLastPrice(client *binance.Client, pair string) (string, error) {
	// fmt.Printf("Will get price for: %s", pair)
	fmt.Println("")
	prices, err := client.NewListPricesService().Do(context.Background())
	if err != nil {
		fmt.Printf("Get Price error: %v\n", err)
		return "", err
	}

	for _, p := range prices {
		if p.Symbol == pair {
			return p.Price, nil
		}
	}

	return "", errors.New("Not found")
}
