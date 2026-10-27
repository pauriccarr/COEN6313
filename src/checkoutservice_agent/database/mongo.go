package database

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var OrdersCollection *mongo.Collection

func ConnectMongo(uri string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(
		options.Client().ApplyURI(uri),
	)

	if err != nil {
		return err
	}

	if err := client.Ping(ctx, nil); err != nil {
		return err
	}

	db := client.Database("checkout_agent_db")
	OrdersCollection = db.Collection("orders")

	return nil
}
