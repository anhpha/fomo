package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"pumpdump/helper"
	"time"

	"github.com/go-redis/redis"

	binanceLib "github.com/adshao/go-binance"
)

type binance struct {
	key         string
	secret      string
	worker      *binanceLib.Client
	exchageInfo binanceLib.ExchangeInfo
}

const (
	LOT_SIZE     = "LOT_SIZE"
	PRICE_FILTER = "PRICE_FILTER"
	BTCUSDT      = "BTCUSDT"
)

// NewBinance init a binance instance
func NewBinance(key string, secret string) Market {
	return &binance{
		key:    key,
		secret: secret,
		worker: &binanceLib.Client{
			APIKey:     key,
			SecretKey:  secret,
			BaseURL:    "https://api.binance.com",
			UserAgent:  "curl/7.47.0",
			HTTPClient: http.DefaultClient,
			Debug:      false,
			Logger:     log.New(os.Stdout, "Binance-golang ", log.LstdFlags),
		},
	}
}

func (b *binance) GetExchangeInfo() (interface{}, error) {
	infos, err := b.worker.NewExchangeInfoService().Do(context.Background())
	// fmt.Printf("Info: %v\n", infos)
	if err == nil {
		b.exchageInfo = *infos
		return infos, nil
	}
	return nil, err
}

func (b *binance) GetPairInfo(pair string) (Pair, error) {
	var result Pair
	for _, s := range b.exchageInfo.Symbols {
		if s.Symbol == pair {
			result = Pair{Symbol: pair}
			for _, filter := range s.Filters {
				switch filter["filterType"] {
				case "PRICE_FILTER":
					result.PriceFilter = PriceFilter{
						Min:  helper.StringToFloat64(filter["minPrice"]),
						Max:  helper.StringToFloat64(filter["maxPrice"]),
						Tick: helper.StringToFloat64(filter["tickSize"]),
					}
				case "LOT_SIZE":
					result.LotSize = LotSize{
						Min:  helper.StringToFloat64(filter["minQty"]),
						Max:  helper.StringToFloat64(filter["maxQty"]),
						Step: helper.StringToFloat64(filter["stepSize"]),
					}
				case "MIN_NOTIONAL":
					result.MinNotional = MinNotional{Value: helper.StringToFloat64(filter["minNotional"])}
				}
			}
			return result, nil
		}
	}
	return result, errors.New("Not found")
}

func (b *binance) GetPairCurrentBestPrice(pair string) (BestPrice, error) {
	var result BestPrice
	data, err := b.worker.NewBookTickerService().Symbol(pair).Do(context.Background())
	if err == nil {
		result.Symbol = pair
		result.Ask = helper.StringToFloat64(data.AskQuantity)
		result.AskPrice = helper.StringToFloat64(data.AskPrice)
		result.Bid = helper.StringToFloat64(data.BidQuantity)
		result.BidPrice = helper.StringToFloat64(data.BidPrice)
	}
	return result, err
}

func (b *binance) tryToGetCurrentBestPrice(pair string, maxTry int, delay int) (BestPrice, error) {
	var result BestPrice
	tried := 0
	done := 0
	for done == 0 && tried < maxTry {
		p, err := b.GetPairCurrentBestPrice(pair)
		if err == nil {
			done = 1
			return p, err
		}
	}
	return result, fmt.Errorf("Cannot get current best price for %s", pair)
}

type executedOrder struct {
	orderID              int64
	symbol               string
	price                float64
	lastExecutedQuantity float64
	lastExecutedPrice    float64
	currentOrderStatus   string
}

func getExecutedOrderFromStream(data []byte, orderID int64) (executedOrder, error) {
	var result executedOrder

	var jsonData map[string]interface{}
	if err := json.Unmarshal(data, &jsonData); err != nil {
		return result, err
	}
	eventType, ok := jsonData["e"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	if eventType != "executionReport" {
		return result, errors.New("Invalid event")
	}
	i, ok := jsonData["i"].(int64)
	if !ok || i != orderID {
		return result, errors.New("Invalid event")
	}
	s, ok := jsonData["s"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	L, ok := jsonData["L"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	p, ok := jsonData["p"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	l, ok := jsonData["l"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	X, ok := jsonData["X"].(string)
	if !ok {
		return result, errors.New("Invalid event")
	}
	result.orderID = i
	result.symbol = s
	result.price = helper.StringToFloat64(p)
	result.lastExecutedPrice = helper.StringToFloat64(L)
	result.lastExecutedQuantity = helper.StringToFloat64(l)
	result.currentOrderStatus = X
	return result, nil
}

func (b *binance) oderInfoChanel() (data chan []byte, doneC chan struct{}, stopC chan struct{}, listenKey string, e error) {
	data = make(chan []byte)

	listenKey, err := b.worker.NewStartUserStreamService().Do(context.Background())
	if err != nil {
		return data, doneC, stopC, listenKey, err
	}

	wsHandler := func(message []byte) {
		// // for {
		// select {
		// case orderID := <-orderIDChan:
		// 	monitoringID := orderID
		// default:
		// 	executedOrder, err := getExecutedOrderFromStream(message, monitoringID)
		// 	if err == nil {
		// 		data <- executedOrder
		// 	}
		// }
		// }
		data <- message

	}
	errHandler := func(err error) {
		fmt.Println(err)
	}
	doneC, stopC, err = binanceLib.WsUserDataServe(listenKey, wsHandler, errHandler)
	if err != nil {
		fmt.Println(err)
		return data, doneC, stopC, listenKey, err
	}
	fmt.Println("Started user stream...")

	// <-doneC
	return
}

func (b *binance) Fomo(pair string, amount float64, buyPrice float64, maxPrice float64,
	tk float64, sl float64, race int, delay int, c chan error) (terminater chan error, e error) {

	// var openOderID int64
	// openOderID = 0
	// done := 0

	pairInfo, err := b.GetPairInfo(pair)

	// fmt.Printf("Will fomo on %v\n", pairInfo)
	// fmt.Printf("Min notional %v\n", pairInfo.MinNotional.Value)

	if err != nil {
		fmt.Printf("Cannot get symbol info %s\n", pair)
		c <- fmt.Errorf("Cannot get pair info: %v", err)
		return
	}

	if buyPrice == 0 {
		marketSellPrice, err := b.tryToGetCurrentBestPrice(pair, 10, 200)
		if err != nil {
			fmt.Printf("Cannot get symbol info %s", err)
			c <- err
			return nil, err
		}
		buyPrice = marketSellPrice.BidPrice
	} else {
		// Correct buy price
		buyPrice = buyPrice - math.Mod(buyPrice, pairInfo.PriceFilter.Tick)
	}
	// Default will not buy if price increase more than 1%
	if maxPrice == 0 {
		maxPrice = buyPrice * 1.1
	}

	maxBuyPrice := amount / buyPrice

	willBuyAmout := maxBuyPrice - math.Mod(maxBuyPrice, pairInfo.LotSize.Step)
	// hasError := 0
	// needToBuyMore := 1

	fmt.Printf("Will try to buy %v (%v USDT) @ %v\n", willBuyAmout, willBuyAmout*buyPrice, buyPrice)
	// newOrder, err := b.tryToSetLimitOrder(pair, willBuyAmout, buyPrice, maxPrice, race, delay)
	order, err := b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair,
		helper.Float64ToString(buyPrice), helper.Float64ToString(willBuyAmout))
	if err != nil {
		fmt.Printf("Cannot set buy order: %v\n", err)
		return nil, err
	}
	bestPriceChan, _, priceStopC, err := b.GetBestPriceChanel(pair)

	if err != nil {
		fmt.Printf("Cannot listen price for %s\n", pair)
		c <- fmt.Errorf("Cannot listen price: %v", err)
		return nil, err
	}
	bookingOrderID := order.OrderID
	bookingPrice := helper.StringToFloat64(order.Price)
	currentBestPrice := buyPrice
	filledAmount := 0.0
	var currentExecutedOrder executedOrder

	startedStoploss := 0

	terminater = make(chan error, 2)
	userStream, _, stopC, listenKey, err := b.oderInfoChanel()
	if err == nil {
		go func() {
			defer close(userStream)
			// defer close(terminater)
			done := 0
			for done == 0 {
				fmt.Println("Listening...")
				select {
				case cause := <-terminater:
					fmt.Printf("Stop stream caused by: %s", cause.Error())
					b.worker.NewCloseUserStreamService().ListenKey(listenKey).Do(context.Background())
					priceStopC <- struct{}{}
					stopC <- struct{}{}

					done = 1
					return
				case data := <-userStream:
					currentExecutedOrder, err = getExecutedOrderFromStream(data, bookingOrderID)
					if err == nil {
						// Update filled amount
						filledAmount += currentExecutedOrder.lastExecutedQuantity
						// Protect filled buy monitorig and stoploss. Only start 1 time
						if (currentExecutedOrder.currentOrderStatus == "FILLED" ||
							currentExecutedOrder.currentOrderStatus == "PARTIALLY_FILLED") && startedStoploss == 0 {
							go b.tryToStopLossForOpenOders(pair, sl, 0, delay, c)
							startedStoploss = 1
						}
					}
				default:
					bestPrice := <-bestPriceChan
					currentBestPrice = bestPrice.bid
					if currentBestPrice > bookingPrice && race == 1 && currentBestPrice <= maxBuyPrice {
						// Truy to update order
						err := b.tryToCancelOrder(pair, bookingOrderID, 100)
						if err != nil {
							fmt.Printf("Cannot cancel opening order: %v", err)
							b.worker.NewCloseUserStreamService().ListenKey(listenKey).Do(context.Background())
							priceStopC <- struct{}{}
							stopC <- struct{}{}

							done = 1
							return
						}
						newOrder, err := b.tryToSetLimitOrder(pair, willBuyAmout-filledAmount, currentBestPrice, delay)
						if err != nil {
							fmt.Printf("Cannot cancel opening order: %v", err)
							b.worker.NewCloseUserStreamService().ListenKey(listenKey).Do(context.Background())
							priceStopC <- struct{}{}
							stopC <- struct{}{}

							done = 1
							return
						}
						// Update order id for monitoring
						bookingOrderID = newOrder.OrderID
					}

				}
			}
		}()
	} else {
		fmt.Printf("Cannot start user stream: %v\n", err)
	}
	return
}

func (b *binance) TryToSetTakeProfitAndStopLost(pairInfo Pair, filledPrice float64, amount float64, tk float64, sl float64, maxTry int, delay int, contact chan error) error {
	done := 0
	tried := 0

	profitPrice := filledPrice + b.CalculateChangePrice(filledPrice, tk, pairInfo.PriceFilter.Tick, true)
	lossPrice := filledPrice - b.CalculateChangePrice(filledPrice, sl, pairInfo.PriceFilter.Tick, false)

	fmt.Printf("Monitoring for  %s @ %v with profit @ %v and loss @ %v\n", pairInfo.Symbol, filledPrice, profitPrice, lossPrice)

	for done != 1 && tried <= maxTry {
		fmt.Printf("Try to take profit for %v @ %v (%v USDT)\n", amount, profitPrice, amount*profitPrice)
		profitOrder, err := b.createLimitOrder(binanceLib.SideTypeSell, binanceLib.OrderTypeLimit, pairInfo.Symbol, helper.Float64ToString(profitPrice), helper.Float64ToString(amount))
		if err == nil {
			fmt.Printf("Will take profit with %v\n", profitOrder)

			b.MonitorAndStopLoss(pairInfo.Symbol, profitOrder, lossPrice, delay, contact)
			done = 1
		} else {
			fmt.Printf("Error when setting profit order:%v\n", err)
		}
		tried++
		wait := time.Duration(delay) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil
}

func (b *binance) MonitorAndStopLoss(pair string, order *binanceLib.CreateOrderResponse, stopPrice float64, interval int, contact chan error) error {
	fmt.Printf("Motitoring pair: %s\n", pair)
	done := 0

	needToStoploss := 0
	needCancelOldOrder := 1

	for done != 1 {
		hasError := 0
		currentOrder, err := b.GetOrderInfo(pair, order.OrderID, 100)
		if err == nil {
			hasError = 0
			if currentOrder.Status == "FILLED" {
				fmt.Printf("*** Taken profit @ %v", currentOrder.Price)
				done = 1
				needToStoploss = 0
				return nil
			}
		} else {
			hasError = 1

		}
		// Check price
		if needToStoploss == 0 && hasError == 0 && done != 1 {
			bestPrice, err := b.GetPairCurrentBestPrice(pair)
			if err == nil {
				hasError = 0
				if bestPrice.BidPrice <= stopPrice {
					needToStoploss = 1
				}
			} else {
				hasError = 1
			}
		}
		if needToStoploss == 1 && hasError == 0 && done != 1 {
			remainAmout := helper.StringToFloat64(order.OrigQuantity) - helper.StringToFloat64(currentOrder.ExecutedQuantity)
			fmt.Printf("Try to stop loss for %v @ %v (%v USDT)\n", remainAmout, stopPrice, remainAmout*stopPrice)
			// Cancel open order
			if needCancelOldOrder == 1 {
				err := b.tryToCancelOrder(pair, order.OrderID, 100)
				if err == nil {
					needCancelOldOrder = 0
					hasError = 0
				} else {
					hasError = 1
				}
			}
			if hasError == 0 {
				// Try to sell at market price
				_, err := b.worker.NewCreateOrderService().Symbol(pair).
					Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(helper.Float64ToString(remainAmout)).Do(context.Background())
				if err == nil {
					done = 1
					// contact <- fmt.Errorf(":(((( Stoploss @ %v", slOrder)
					return nil
				} else {
					fmt.Printf("Error when trying to stoploss: %v\n", err)
				}
			}
		}
		wait := time.Duration(interval) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}

	return fmt.Errorf("Cannot do stoploss for %v", order)
}

func (b *binance) GetOrderInfo(pair string, orderID int64, maxTry int) (*binanceLib.Order, error) {
	// var result binanceLib.Order
	ok := 0
	tried := 0
	for ok == 0 && tried <= maxTry {
		order, err := b.worker.NewGetOrderService().Symbol(pair).OrderID(orderID).Do(context.Background())
		if err == nil {
			ok = 1
			return order, nil
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tried++
	}
	return nil, errors.New("Cannot get order status")
}

// Incase race, buyPrice will be ignore
func (b *binance) tryToSetLimitOrder(pair string, amount float64, buyPrice float64, interval int) (*binanceLib.CreateOrderResponse, error) {
	ok := 0
	for ok == 0 {
		order, err := b.createLimitOrder(binanceLib.SideTypeBuy, binanceLib.OrderTypeLimit, pair, helper.Float64ToString(buyPrice), helper.Float64ToString(amount))
		if err == nil {
			ok = 1
			return order, err
		}
		fmt.Printf("Error when trying to buy: %v\n", err)
		// try again after 0.2 s
		wait := time.Duration(interval) * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
	}
	return nil, errors.New("Cannot put new order")
}

func (b *binance) tryToCancelOrder(pair string, orderID int64, maxTry int) error {
	ok := 0
	tried := 0
	for ok == 0 && tried <= maxTry {
		_, err := b.worker.NewCancelOrderService().Symbol(pair).OrderID(orderID).Do(context.Background())
		if err == nil {
			ok = 1
			return nil
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		// try again after 0.2 s
		time.Sleep(wait)
		tried++
	}
	return fmt.Errorf("Cannot cancel order %d after %d times", orderID, tried)
}

func (b *binance) createLimitOrder(orderSide binanceLib.SideType, orderType binanceLib.OrderType, pair string, price string, quantity string) (*binanceLib.CreateOrderResponse, error) {
	s := b.worker.NewCreateOrderService().Symbol(pair).
		Side(orderSide).Type(orderType).Quantity(quantity)

	if orderType == "STOP_LOSS_LIMIT" || orderType == "TAKE_PROFIT_LIMIT" || orderType == "LIMIT" {
		s = s.TimeInForce(binanceLib.TimeInForceGTC).Price(price)
	}
	return s.Do(context.Background())
}

func (b *binance) CalculateChangePrice(originalPrice float64, percent float64, tickSize float64, increase bool) float64 {
	idealChange := originalPrice
	if increase == true {
		idealChange = originalPrice * (1 + percent)
	} else {
		idealChange = originalPrice * (1 - percent)
	}
	delta := math.Abs(idealChange - originalPrice)
	return delta - math.Mod(delta, tickSize)

}

func (b *binance) tryToGetListOpenOrders(pair string, try int) ([]*binanceLib.Order, error) {

	var empty []*binanceLib.Order

	ok := 0
	tried := 0
	for ok == 0 && tried < try {
		results, err := b.worker.NewListOpenOrdersService().Symbol(pair).Do(context.Background())
		if err == nil {
			ok = 1
			return results, err
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return empty, fmt.Errorf("Cannot get list opening orders for pair %s", pair)
}

func getBestPriceFromDepthEvent(event *binanceLib.WsPartialDepthEvent) (float64, error) {
	bestPrice := 0.0
	if len(event.Bids) == 0 {
		return bestPrice, errors.New("Empty data from server")
	}
	bestPrice = helper.StringToFloat64(event.Bids[0].Price)
	return bestPrice, nil
}

func (b *binance) monitorBTC(client *redis.Client) chan struct{} {
	bestBTCPriceChan, _, stopC, err := b.GetBestPriceChanel(BTCUSDT)
	if err != nil {
		fmt.Println("Cannot monitor BTC")
		panic(err)
	}
	go func() {
		for {
			select {
			case <-stopC:
				return
			default:
				currentBestPrice := <-bestBTCPriceChan
				err := client.Set(BTCUSDT, currentBestPrice.bid, 0).Err()
				if err != nil {
					fmt.Printf("Cannot update BTC: %v", err)
				}
			}
		}
	}()
	return stopC
}

func (b *binance) getBTCUSDTFromRedis(c *redis.Client) float64 {
	val, err := c.Get(BTCUSDT).Result()
	if err != nil {
		fmt.Printf("Cannot get BTC price from Redis. Using 10M")
		return 1000000.0
	}
	return helper.StringToFloat64(val)
}

func (b *binance) MonitorAndStopLossOrder(pair string, sl float64, slBTC float64, delay int, terminater chan error) []error {
	controlC, errs := b.tryToStopLossForOpenOders(pair, sl, slBTC, delay, terminater)
	go func() {
		select {
		case <-time.After(12 * time.Hour):
			fmt.Println("Restarting process")
			controlC <- "Restart process"
			controlC, errs = b.tryToStopLossForOpenOders(pair, sl, slBTC, delay, terminater)
		}
	}()
	return errs
}

func (b *binance) tryToStopLossForOpenOders(pair string, sl float64, slBTC float64, delay int, terminater chan error) (chan string, []error) {
	var results []error
	fc := make(chan string)
	controlChanel := make(chan string)
	// done := 0
	doneSL := 0
	age := 0

	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	// client.
	_, err := client.Ping().Result()
	if err != nil {
		fmt.Println("Cannot monitor BTC")
		panic(err)
	}

	stopMoniroBTCChan := b.monitorBTC(client)

	wsDepthHandler := func(event *binanceLib.WsPartialDepthEvent) {
		age++
		bestPirce, err := getBestPriceFromDepthEvent(event)
		bestBTCPrice := b.getBTCUSDTFromRedis(client)
		// fmt.Printf("Best BTC Price : %v, BTC Stoploss: %v\n", bestBTCPrice, slBTC)
		// fmt.Printf("Best Price : %v, Stoploss: %v\n", bestPirce, sl)
		if err == nil {
			if age%30 == 0 {
				fmt.Printf("Best BTC Price : %v, BTC Stoploss: %v\n", bestBTCPrice, slBTC)
				fmt.Printf("Best %s Price : %v, Stoploss: %v\n", pair, bestPirce, sl)
				fmt.Println("==============================")
			}
			if (bestPirce <= sl) || (slBTC > 0 && slBTC >= bestBTCPrice) {
				for doneSL == 0 {
					var errs []error
					// errs = []{}
					fmt.Println("Trying to stoploss")
					orders, err := b.tryToGetListOpenOrders(pair, 10)
					if err == nil {
						for _, order := range orders {
							err = b.tryToCancelOrder(pair, order.OrderID, 10)
							if err == nil {
								remainAmout := helper.StringToFloat64(order.OrigQuantity) - helper.StringToFloat64(order.ExecutedQuantity)
								err = b.tryToSellAnyway(pair, remainAmout)
								if err != nil {
									fmt.Println(err)
									errs = append(errs, err)
								}
							} else {
								errs = append(errs, err)
							}
						}
					} else {
						fmt.Println(err)
						errs = append(errs, err)
					}

					if len(errs) == 0 {
						doneSL = 1
					} else {
						wait := 1000 * time.Millisecond
						time.Sleep(wait)
					}

				}
				if doneSL == 1 && slBTC >= bestBTCPrice {
					err = b.tryToSellAllBTCAnyway()
					if err == nil {
						fmt.Println("Sell all btc")
					} else {
						fmt.Println("Cannot sell all btc")
					}
				}
				fc <- "**** Done stop loss"
				// stopMoniroBTCChan <- struct{}{}
				// terminater <- fmt.Errorf("%s", "**** Done stop loss")

			}

		} else {
			fmt.Println(err)
		}

	}
	errHandler := func(err error) {
		results = append(results, err)
		fmt.Println(err)
		fc <- fmt.Sprintf("%s", err)
	}
	_, stopC, err := binanceLib.WsPartialDepthServe(pair, "5", wsDepthHandler, errHandler)
	if err != nil {
		fmt.Println(err)
		results = append(results, err)
		// stopC <- struct{}{}
		stopMoniroBTCChan <- struct{}{}
		terminater <- fmt.Errorf("%s", err)
		return controlChanel, results
	}

	go func() {
		defer close(fc)
		defer close(controlChanel)
		defer client.Close()
		for {
			select {
			case result := <-fc:
				fmt.Println(result)
				stopMoniroBTCChan <- struct{}{}
				stopC <- struct{}{}
				terminater <- fmt.Errorf("%s", "**** Done stop loss")
				return
			case <-controlChanel:
				stopMoniroBTCChan <- struct{}{}
				stopC <- struct{}{}
				return
			}
		}
	}()

	// fmt.Printf("Stop monitoring: %v\n", <-fc)
	// stopMoniroBTCChan <- struct{}{}
	// stopC <- struct{}{}
	// terminater <- fmt.Errorf("%s", "**** Done stop loss")
	return controlChanel, results
}

func (b *binance) tryToSellAnyway(pair string, amount float64) error {
	ok := 0
	tried := 0
	for ok == 0 && tried < 10 {
		order, err := b.worker.NewCreateOrderService().Symbol(pair).
			Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(helper.Float64ToString(amount)).Do(context.Background())
		if err == nil {
			ok = 1
			fmt.Printf("Sold %s by order ID: %v", pair, order.OrderID)
			return nil
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return fmt.Errorf("Cannot sell pair %s anyway", pair)
}

func (b *binance) tryToSellAllBTCAnyway() error {
	ok := 0
	tried := 0
	for ok == 0 && tried < 10 {
		btcAmount, err := b.tryToGetBalance("BTC")
		if err == nil {
			order, err := b.worker.NewCreateOrderService().Symbol("BTCUSDT").
				Side(binanceLib.SideTypeSell).Type(binanceLib.OrderTypeMarket).Quantity(helper.Float64ToString(btcAmount)).Do(context.Background())
			if err == nil {
				ok = 1
				fmt.Printf("Sold BTC by order ID: %v", order.OrderID)
				return nil
			}
		}

		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return fmt.Errorf("Cannot sell pair %s anyway", "BTC")
}

// BestPriceNow best price for asks and bids
type BestPriceNow struct {
	ask float64
	bid float64
}

func getBestPriceNowFromDepthEvent(event *binanceLib.WsPartialDepthEvent) (BestPriceNow, error) {
	var result BestPriceNow
	if len(event.Bids) == 0 {
		return result, errors.New("Empty data from server")
	}
	result = BestPriceNow{
		ask: helper.StringToFloat64(event.Asks[0].Price), bid: helper.StringToFloat64(event.Bids[0].Price),
	}
	return result, nil
}

// GetBestPriceChanel update price using websocket
func (b *binance) GetBestPriceChanel(pair string) (chan BestPriceNow, chan struct{}, chan struct{}, error) {
	c := make(chan BestPriceNow)
	wsDepthHandler := func(event *binanceLib.WsPartialDepthEvent) {
		bestPirce, err := getBestPriceNowFromDepthEvent(event)
		if err == nil {
			c <- bestPirce
		} else {
			fmt.Println(err)
		}

	}
	errHandler := func(err error) {
		fmt.Printf("Websocket error: %v\n", err)
	}
	doneC, stopC, err := binanceLib.WsPartialDepthServe(pair, "5", wsDepthHandler, errHandler)
	if err != nil {
		fmt.Printf("Websocket error: %v\n", err)
		return c, doneC, stopC, err
	}
	return c, doneC, stopC, nil
}

func (b *binance) tryToGetBalance(pair string) (float64, error) {
	ok := 0
	tried := 0
	for ok == 0 && tried < 10 {
		acc, err := b.worker.NewGetAccountService().Do(context.Background())
		if err == nil {
			for _, balance := range acc.Balances {
				if pair == balance.Asset {
					ok = 1
					return helper.StringToFloat64(balance.Free), nil
				}
			}
			return 0, nil
		}
		// try again after 0.2 s
		wait := 500 * time.Millisecond
		time.Sleep(wait)
		tried++
	}
	return 0, fmt.Errorf("Cannot sell pair %s anyway", pair)
}
