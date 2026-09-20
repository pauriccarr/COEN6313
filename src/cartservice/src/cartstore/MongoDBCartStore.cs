using System.Linq;
using System.Threading.Tasks;
using MongoDB.Bson;
using MongoDB.Driver;
using Microsoft.Extensions.Configuration;

namespace cartservice.cartstore
{
    public class MongoDBCartStore : ICartStore
    {
        private readonly IMongoCollection<CartDocument> _cartCollection;

        public MongoDBCartStore(IConfiguration configuration)
        {
            string connectionString = configuration["MONGODB_CONNECTION_STRING"];
            string databaseName = configuration["MONGODB_DATABASE_NAME"] ?? "cartdb";

            var client = new MongoClient(connectionString);
            var database = client.GetDatabase(databaseName);
            _cartCollection = database.GetCollection<CartDocument>("carts");

            InitializeAsync().GetAwaiter().GetResult();
        }

        public Task InitializeAsync()
        {
            var indexKeys = Builders<CartDocument>.IndexKeys.Ascending(c => c.UserId);
            return _cartCollection.Indexes.CreateOneAsync(new CreateIndexModel<CartDocument>(indexKeys));
        }



        public async Task AddItemAsync(string userId, string productId, int quantity)
        {
            var filter = Builders<CartDocument>.Filter.And(
                Builders<CartDocument>.Filter.Eq(c => c.UserId, userId),
                Builders<CartDocument>.Filter.ElemMatch(c => c.Items, i => i.ProductId == productId));

            var incUpdate = Builders<CartDocument>.Update.Inc("items.$.quantity", quantity);

            var result = await _cartCollection.UpdateOneAsync(filter, incUpdate);

            if (result.MatchedCount == 0)
            {
                // Either the cart doesn't exist yet, or it exists but doesn't have this product.
                var pushUpdate = Builders<CartDocument>.Update.Push(c => c.Items,
                    new CartItemDocument { ProductId = productId, Quantity = quantity });

                await _cartCollection.UpdateOneAsync(
                    c => c.UserId == userId,
                    pushUpdate,
                    new UpdateOptions { IsUpsert = true });
            }
        }

        public async Task EmptyCartAsync(string userId)
        {
            await _cartCollection.DeleteOneAsync(c => c.UserId == userId);
        }

        public async Task<Hipstershop.Cart> GetCartAsync(string userId)
        {
            var doc = await _cartCollection.Find(c => c.UserId == userId).FirstOrDefaultAsync();

            var cart = new Hipstershop.Cart { UserId = userId };
            if (doc != null)
            {
                foreach (var item in doc.Items)
                {
                    cart.Items.Add(new Hipstershop.CartItem
                    {
                        ProductId = item.ProductId,
                        Quantity = item.Quantity
                    });
                }
            }
            return cart;
        }

        public bool Ping()
        {
            try
            {
                _cartCollection.Database.RunCommand((Command<BsonDocument>)"{ping:1}");
                return true;
            }
            catch
            {
                return false;
            }
        }
    }
}
