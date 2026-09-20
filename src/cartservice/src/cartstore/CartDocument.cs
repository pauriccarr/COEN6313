using System.Collections.Generic;
using MongoDB.Bson.Serialization.Attributes;

namespace cartservice.cartstore
{
    public class CartItemDocument
    {
        [BsonElement("productId")]
        public string ProductId { get; set; }

        [BsonElement("quantity")]
        public int Quantity { get; set; }
    }

    public class CartDocument
    {
        [BsonId]
        public string UserId { get; set; }

        [BsonElement("items")]
        public List<CartItemDocument> Items { get; set; } = new List<CartItemDocument>();
    }
}
