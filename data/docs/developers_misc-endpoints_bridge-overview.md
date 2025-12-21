# Overview - Polymarket Documentation

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
Bridge & Swap
Overview
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
USDC.e on Polygon
Base URL
Key Features
Endpoints
Bridge & Swap
Overview
Bridge and swap assets to Polymarket
​
Overview


The Polymarket Bridge API enables seamless deposits between multiple blockchains and Polymarket.


​
USDC.e on Polygon


Polymarket uses USDC.e (Bridged USDC) on Polygon as collateral
 for all trading activities. USDC.e is the bridged version of USDC from Ethereum, and it serves as the native currency for placing orders and settling trades on Polymarket.


When you deposit assets to Polymarket:




You can deposit from various supported chains (Ethereum, Solana, Arbitrum, Base, etc.)


Your assets are automatically bridged/swapped to USDC.e on Polygon


USDC.e is credited to your Polymarket wallet


You can now trade on any Polymarket market




​
Base URL


Copy
Ask AI
https://bridge.polymarket.com




​
Key Features




Multi-chain deposits
: Bridge assets from Ethereum, Solana, Bitcoin, Arbitrum, Base, and other supported chains


Automatic conversion
: Assets are automatically bridged/swapped to USDC.e on Polygon




​
Endpoints




POST /deposit
 - Create unique deposit addresses for bridging assets


GET /supported-assets
 - Get all supported chains and tokens


Get daily builder volume time-series
Create Deposit
⌘
I
github
Powered by Mintlify