namespace Acme.Pricing
{
    /// <summary>Prices bonds.</summary>
    public class BondPricer
    {
        /// <summary>Creates a pricer.</summary>
        public BondPricer(string curve) { }

        /// <summary>Returns the clean price.</summary>
        public decimal CleanPrice(decimal coupon, int years)
        {
            var key = Environment.GetEnvironmentVariable("PRICER_KEY");
            return 0m;
        }

        private void Hidden() { }
    }
}
