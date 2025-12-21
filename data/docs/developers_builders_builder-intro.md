# Builder Program Introduction - Polymarket Documentation

Skip to main content
Polymarket Documentation
 home page
Search...
⌘
K
Main Site
Main Site
Search...
Navigation
Polymarket Builders Program
Builder Program Introduction
User Guide
For Developers
Changelog
Polymarket
Discord Community
Twitter
Developer Quickstart
Developer Quickstart
Your First Order
Glossary
API Rate Limits
Endpoints
Polymarket Builders Program
Builder Program Introduction
Builder Profile & Keys
Order Attribution
Relayer Client
Examples
Central Limit Order Book
CLOB Introduction
Status
Quickstart
Authentication
Client
REST API
Historical Timeseries Data
Order Management
Trades
Websocket
WSS Overview
WSS Quickstart
WSS Authentication
User Channel
Market Channel
Real Time Data Stream
RTDS Overview
RTDS Crypto Prices
RTDS Comments
Gamma Structure
Overview
Gamma Structure
Fetching Markets
Gamma Endpoints
Health
Sports
Tags
Events
Markets
Series
Comments
Search
Data-API
Health
Core
Misc
Builders
Bridge & Swap
Overview
POST
Create Deposit
GET
Get Supported Assets
Subgraph
Overview
Resolution
Resolution
Rewards
Liquidity Rewards
Conditional Token Frameworks
Overview
Splitting USDC
Merging Tokens
Reedeeming Tokens
Deployment and Additional Information
Proxy Wallets
Proxy wallet
Negative Risk
Overview
On this page
What is a Builder?
Program Benefits
Relayer Access
Trading Attribution
Getting Started
SDKs & Libraries
Polymarket Builders Program
Builder Program Introduction
Learn about Polymarket’s Builder Program and how to integrate
​
What is a Builder?


A “builder” is a person, group, or organization that routes orders from their users to Polymarket.
If you’ve created a platform that allows users to trade on Polymarket via your system, this program is for you.




​
Program Benefits


Relayer Access
All onchain operations are gasless through our relayer
Order Attribution
Get credited for orders and compete for grants on the Builder Leaderboard
Fee Share
Earn a share of fees on routed orders


​
Relayer Access


We expose our relayer to builders, providing gasless transactions for users with
Polymarket’s Proxy Wallets deployed via 
Relayer Client
.


When transactions are routed through proxy wallets, Polymarket pays all gas fees for:




Deploying Gnosis Safe Wallets or Custom Proxy (Magic Link users) Wallets


Token approvals (USDC, outcome tokens)


CTF operations (split, merge, redeem)


Order execution (via 
CLOB API
)




EOA wallets do not have relayer access. Users trading directly from an EOA pay their own gas fees.


​
Trading Attribution


Attach custom headers to orders to identify your builder account:




Orders attributed to your builder account


Compete on the 
Builder Leaderboard
 for grants


Track performance via the Data API




Leaderboard API
: Get aggregated builder rankings for a time period


Volume API
: Get daily time-series volume data for trend analysis










​
Getting Started




Get Builder Credentials
: Generate API keys from your 
Builder Profile


Configure Order Attribution
: Set up CLOB client to credit trades to your account (
guide
)


Enable Gasless Transactions
: Use the Relayer for gas-free wallet deployment and trading (
guide
)




See 
Example Apps
 for complete Next.js reference implementations.




​
SDKs & Libraries


CLOB Client (TypeScript)
Place orders with builder attribution
CLOB Client (Python)
Place orders with builder attribution
Relayer Client (TypeScript)
Gasless onchain transactions for your users
Relayer Client (Python)
Gasless onchain transactions for your users
Signing SDK (TypeScript)
Sign builder authentication headers
Signing SDK (Python)
Sign builder authentication headers
Endpoints
Builder Profile & Keys
⌘
I
github
Powered by Mintlify