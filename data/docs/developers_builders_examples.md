# Examples - Polymarket Documentation

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
Examples
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
Overview
Safe Wallet Examples
What Each Demo Covers
Polymarket Builders Program
Examples
Complete Next.js applications demonstrating Polymarket builder integration
​
Overview


These open-source demo applications show how to integrate Polymarket’s CLOB Client and Builder Relayer Client for gasless trading with builder order attribution.


Authentication
Multiple wallet providers
Gasless Trading
Safe & Proxy wallet support
Full Integration
Orders, positions, CTF ops




​
Safe Wallet Examples


Deploy Gnosis Safe wallets for your users:


wagmi + Safe
MetaMask, Phantom, Rabby, and other browser wallets
Privy + Safe
Privy embedded wallets
Magic Link + Safe
Magic Link email/social authentication
Turnkey + Safe
Turnkey embedded wallets




​
What Each Demo Covers


 
Authentication
 
Wallet Operations
 
Trading


User sign-in via wallet provider


User API credential derivation (L2 auth)


Builder config with remote signing


Signature types for Safe vs Proxy wallets




Safe wallet deployment via Relayer


Batch token approvals (USDC.e + outcome tokens)


CTF operations (split, merge, redeem)


Transaction monitoring




CLOB client initialization


Order placement with builder attribution


Position and order management


Market discovery via Gamma API


Relayer Client
CLOB Introduction
⌘
I
github
Powered by Mintlify