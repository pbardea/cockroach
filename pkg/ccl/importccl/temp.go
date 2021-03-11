package importccl

const schemaStmt string = `
CREATE DATABASE tpce;
USE tpce;
CREATE TABLE account_permission (
    ap_ca_id  INT8 NOT NULL,
    ap_acl    VARCHAR(4) NOT NULL,
    ap_tax_id VARCHAR(20) NOT NULL,
    ap_l_name VARCHAR(25) NOT NULL,
    ap_f_name VARCHAR(20) NOT NULL,
    PRIMARY KEY (ap_ca_id, ap_tax_id)
);
CREATE TABLE customer (
    c_id      INT8 NOT NULL PRIMARY KEY,
    c_tax_id  VARCHAR(20) NOT NULL,
    c_st_id   VARCHAR(4) NOT NULL,
    c_l_name  VARCHAR(25) NOT NULL,
    c_f_name  VARCHAR(20) NOT NULL,
    c_m_name  CHAR,
    c_gndr    CHAR,
    c_tier    INT2 NOT NULL CHECK (c_tier IN (1, 2, 3)),
    c_dob     DATE NOT NULL,
    c_ad_id   INT8 NOT NULL,
    c_ctry_1  VARCHAR(3),
    c_area_1  VARCHAR(3),
    c_local_1 VARCHAR(10),
    c_ext_1   VARCHAR(5),
    c_ctry_2  VARCHAR(3),
    c_area_2  VARCHAR(3),
    c_local_2 VARCHAR(10),
    c_ext_2   VARCHAR(5),
    c_ctry_3  VARCHAR(3),
    c_area_3  VARCHAR(3),
    c_local_3 VARCHAR(10),
    c_ext_3   VARCHAR(5),
    c_email_1 VARCHAR(50),
    c_email_2 VARCHAR(50)
);
CREATE TABLE customer_account (
    ca_id     INT8 NOT NULL PRIMARY KEY,
    ca_b_id   INT8 NOT NULL,
    ca_c_id   INT8 NOT NULL,
    ca_name   VARCHAR(50),
    ca_tax_st INT2 NOT NULL CHECK (ca_tax_st IN (0, 1, 2)),
    ca_bal    DECIMAL(12,2) NOT NULL
);
CREATE TABLE customer_taxrate (
    cx_tx_id VARCHAR(4) NOT NULL,
    cx_c_id  INT8 NOT NULL,
    PRIMARY KEY (cx_tx_id, cx_c_id)
);
CREATE TABLE holding (
    h_t_id   INT8 NOT NULL PRIMARY KEY,
    h_ca_id  INT8 NOT NULL,
    h_s_symb VARCHAR(15) NOT NULL,
    h_dts    TIMESTAMP NOT NULL,
    h_price  DECIMAL(8,2) NOT NULL CHECK (h_price > 0),
    h_qty    INT4 NOT NULL
);
CREATE TABLE holding_history (
    hh_h_t_id     INT8 NOT NULL,
    hh_t_id       INT8 NOT NULL,
    hh_before_qty INT4 NOT NULL,
    hh_after_qty  INT4 NOT NULL,
    PRIMARY KEY (hh_h_t_id, hh_t_id)
);
CREATE TABLE holding_summary (
    hs_ca_id  INT8 NOT NULL,
    hs_s_symb VARCHAR(15) NOT NULL,
    hs_qty    INT4 NOT NULL,
    PRIMARY KEY (hs_ca_id, hs_s_symb),
    FAMILY static  (hs_ca_id, hs_s_symb),
    FAMILY dynamic (hs_qty)
);
CREATE TABLE watch_item (
    wi_wl_id  INT8 NOT NULL,
    wi_s_symb VARCHAR(15) NOT NULL,
    PRIMARY KEY (wi_wl_id, wi_s_symb)
);
CREATE TABLE watch_list (
    wl_id   INT8 NOT NULL PRIMARY KEY,
    wl_c_id INT8 NOT NULL
);
CREATE TABLE broker (
    b_id         INT8 NOT NULL PRIMARY KEY,
    b_st_id      VARCHAR(4) NOT NULL,
    b_name       VARCHAR(49) NOT NULL,
    b_num_trades INT8 NOT NULL,
    b_comm_total DECIMAL(12,2) NOT NULL,
    FAMILY static  (b_id, b_st_id, b_name),
    FAMILY dynamic (b_comm_total, b_num_trades)
);
CREATE TABLE cash_transaction (
    ct_t_id INT8 NOT NULL PRIMARY KEY,
    ct_dts  TIMESTAMP NOT NULL,
    ct_amt  DECIMAL(10,2) NOT NULL,
    ct_name VARCHAR(100)
);
CREATE TABLE charge (
    ch_tt_id  VARCHAR(3) NOT NULL,
    ch_c_tier INT2 NOT NULL CHECK (ch_c_tier IN (1, 2, 3)),
    ch_chrg   DECIMAL(10,2) NOT NULL CHECK (ch_chrg >= 0),
    PRIMARY KEY (ch_tt_id, ch_c_tier)
);
CREATE TABLE commission_rate (
    cr_c_tier   INT2 NOT NULL CHECK (cr_c_tier IN (1, 2, 3)),
    cr_tt_id    VARCHAR(3) NOT NULL,
    cr_ex_id    VARCHAR(6) NOT NULL,
    cr_from_qty INT4 NOT NULL CHECK (cr_from_qty >= 0),
    cr_to_qty   INT4 NOT NULL CHECK (cr_to_qty > cr_from_qty),
    cr_rate     DECIMAL(5,2) NOT NULL CHECK (cr_rate >= 0),
    PRIMARY KEY (cr_c_tier, cr_tt_id, cr_ex_id, cr_from_qty)
);
CREATE TABLE settlement (
    se_t_id          INT8 NOT NULL PRIMARY KEY,
    se_cash_type     VARCHAR(40) NOT NULL,
    se_cash_due_date DATE NOT NULL,
    se_amt           DECIMAL(10,2) NOT NULL
);
CREATE TABLE trade (
    t_id          INT8 NOT NULL PRIMARY KEY,
    t_dts         TIMESTAMP NOT NULL,
    t_st_id       VARCHAR(4) NOT NULL,
    t_tt_id       VARCHAR(3) NOT NULL,
    t_is_cash     BOOL NOT NULL,
    t_s_symb      VARCHAR(15) NOT NULL,
    t_qty         INT4 NOT NULL CHECK (t_qty > 0),
    t_bid_price   DECIMAL(8,2) NOT NULL CHECK (t_bid_price > 0),
    t_ca_id       INT8 NOT NULL,
    t_exec_name   VARCHAR(49) NOT NULL,
    t_trade_price DECIMAL(8,2),
    t_chrg        DECIMAL(10,2) NOT NULL CHECK (t_chrg >= 0),
    t_comm        DECIMAL(10,2) NOT NULL CHECK (t_comm >= 0),
    t_tax         DECIMAL(10,2) NOT NULL CHECK (t_tax  >= 0),
    t_lifo        BOOL NOT NULL,
    FAMILY static   (t_id),
    FAMILY dynamic1 (t_comm, t_dts, t_st_id, t_trade_price, t_exec_name, t_tt_id, t_is_cash, t_s_symb, t_qty, t_bid_price, t_ca_id, t_chrg, t_lifo),
    FAMILY dynamic2 (t_tax)
);
CREATE TABLE trade_history (
    th_t_id  INT8 NOT NULL,
    th_dts   TIMESTAMP NOT NULL,
    th_st_id VARCHAR(4) NOT NULL,
    PRIMARY KEY (th_t_id, th_st_id)
);
CREATE TABLE trade_request (
    tr_t_id      INT8 NOT NULL PRIMARY KEY,
    tr_tt_id     VARCHAR(3) NOT NULL,
    tr_s_symb    VARCHAR(15) NOT NULL,
    tr_qty       INT4 NOT NULL CHECK (tr_qty > 0),
    tr_bid_price DECIMAL(8,2) NOT NULL CHECK (tr_bid_price > 0),
    tr_b_id      INT8 NOT NULL
);
CREATE TABLE trade_type (
    tt_id      VARCHAR(3) NOT NULL PRIMARY KEY,
    tt_name    VARCHAR(12) NOT NULL,
    tt_is_sell BOOL NOT NULL,
    tt_is_mrkt BOOL NOT NULL
);
CREATE TABLE company (
    co_id        INT8 NOT NULL PRIMARY KEY,
    co_st_id     VARCHAR(4) NOT NULL,
    co_name      VARCHAR(60) NOT NULL,
    co_in_id     VARCHAR(2) NOT NULL,
    co_sp_rate   VARCHAR(4) NOT NULL,
    co_ceo       VARCHAR(46) NOT NULL,
    co_ad_id     INT8 NOT NULL,
    co_desc      VARCHAR(150) NOT NULL,
    co_open_date DATE NOT NULL
);
CREATE TABLE company_competitor (
    cp_co_id      INT8 NOT NULL,
    cp_comp_co_id INT8 NOT NULL,
    cp_in_id      VARCHAR(2) NOT NULL,
    PRIMARY KEY (cp_co_id, cp_comp_co_id, cp_in_id)
);
CREATE TABLE daily_market (
    dm_date   DATE NOT NULL,
    dm_s_symb VARCHAR(15) NOT NULL,
    dm_close  DECIMAL(8,2) NOT NULL,
    dm_high   DECIMAL(8,2) NOT NULL,
    dm_low    DECIMAL(8,2) NOT NULL,
    dm_vol    INT8 NOT NULL,
    PRIMARY KEY (dm_date, dm_s_symb)
);
CREATE TABLE exchange (
    ex_id       VARCHAR(6) NOT NULL PRIMARY KEY,
    ex_name     VARCHAR(100) NOT NULL,
    ex_num_symb INT4 NOT NULL,
    ex_open     INT2 NOT NULL,
    ex_close    INT2 NOT NULL,
    ex_desc     VARCHAR(150),
    ex_ad_id    INT8 NOT NULL
);
CREATE TABLE financial (
    fi_co_id          INT8 NOT NULL,
    fi_year           INT2 NOT NULL,
    fi_qtr            INT2 NOT NULL CHECK (fi_qtr IN (1, 2, 3, 4)),
    fi_qtr_start_date DATE NOT NULL,
    fi_revenue        DECIMAL(15,2) NOT NULL,
    fi_net_earn       DECIMAL(15,2) NOT NULL,
    fi_basic_eps      DECIMAL(10,2) NOT NULL,
    fi_dilut_eps      DECIMAL(10,2) NOT NULL,
    fi_margin         DECIMAL(10,2) NOT NULL,
    fi_inventory      DECIMAL(15,2) NOT NULL,
    fi_assets         DECIMAL(15,2) NOT NULL,
    fi_liability      DECIMAL(15,2) NOT NULL,
    fi_out_basic      INT8 NOT NULL,
    fi_out_dilut      INT8 NOT NULL,
    PRIMARY KEY (fi_co_id, fi_year, fi_qtr)
);
CREATE TABLE industry (
    in_id    VARCHAR(2) NOT NULL PRIMARY KEY,
    in_name  VARCHAR(50) NOT NULL,
    in_sc_id VARCHAR(2) NOT NULL
);
CREATE TABLE last_trade (
    lt_s_symb     VARCHAR(15) NOT NULL PRIMARY KEY,
    lt_dts        TIMESTAMP NOT NULL,
    lt_price      DECIMAL(8,2) NOT NULL,
    lt_open_price DECIMAL(8,2) NOT NULL,
    lt_vol        INT8 NOT NULL
);
CREATE TABLE news_item (
    ni_id       INT8 NOT NULL PRIMARY KEY,
    ni_headline VARCHAR(80) NOT NULL,
    ni_summary  VARCHAR(255) NOT NULL,
    ni_item     BYTEA NOT NULL,
    ni_dts      TIMESTAMP NOT NULL,
    ni_source   VARCHAR(30) NOT NULL,
    ni_author   VARCHAR(30),
    FAMILY static (ni_id, ni_headline, ni_summary, ni_dts, ni_source, ni_author),
    FAMILY blob   (ni_item)
);
CREATE TABLE news_xref (
    nx_ni_id INT8 NOT NULL,
    nx_co_id INT8 NOT NULL,
    PRIMARY KEY (nx_ni_id, nx_co_id)
);
CREATE TABLE sector (
    sc_id   VARCHAR(2) NOT NULL PRIMARY KEY, 
    sc_name VARCHAR(30) NOT NULL
);
CREATE TABLE security (
    s_symb           VARCHAR(15) NOT NULL PRIMARY KEY,
    s_issue          VARCHAR(6) NOT NULL,
    s_st_id          VARCHAR(4) NOT NULL,
    s_name           VARCHAR(70) NOT NULL,
    s_ex_id          VARCHAR(6) NOT NULL,
    s_co_id          INT8 NOT NULL,
    s_num_out        INT8 NOT NULL,
    s_start_date     DATE NOT NULL,
    s_exch_date      DATE NOT NULL,
    s_pe             DECIMAL(10,2) NOT NULL,
    s_52wk_high      DECIMAL(8,2) NOT NULL,
    s_52wk_high_date DATE NOT NULL,
    s_52wk_low       DECIMAL(8,2) NOT NULL,
    s_52wk_low_date  DATE NOT NULL,
    s_dividend       DECIMAL(10,2) NOT NULL,
    s_yield          DECIMAL(5,2) NOT NULL
);
CREATE TABLE address (
    ad_id      INT8 NOT NULL PRIMARY KEY,
    ad_line1   VARCHAR(80),
    ad_line2   VARCHAR(80),
    ad_zc_code VARCHAR(12) NOT NULL,
    ad_ctry    VARCHAR(80)
);
CREATE TABLE status_type (
    st_id   VARCHAR(4) NOT NULL PRIMARY KEY, 
    st_name VARCHAR(10) NOT NULL
);
CREATE TABLE taxrate (
    tx_id   VARCHAR(4) NOT NULL PRIMARY KEY,
    tx_name VARCHAR(50) NOT NULL,
    tx_rate DECIMAL(6,5) NOT NULL CHECK (tx_rate >= 0)
);
CREATE TABLE zip_code (
    zc_code VARCHAR(12) NOT NULL PRIMARY KEY,
    zc_town VARCHAR(80) NOT NULL,
    zc_div  VARCHAR(80) NOT NULL
);
-- Install Secondary Indexes.
--- Needed for Broker-Volume.
CREATE UNIQUE INDEX ON broker (b_name);                                             -- BROKER_VOLUME
CREATE UNIQUE INDEX ON sector (sc_name);                                            -- BROKER_VOLUME
CREATE INDEX ON industry (in_sc_id);                                                -- BROKER_VOLUME
CREATE INDEX ON trade_request (tr_b_id, tr_s_symb) STORING (tr_qty, tr_bid_price);  -- BROKER_VOLUME
--- Needed for Customer-Position.
CREATE UNIQUE INDEX ON customer (c_tax_id);                                         -- CUSTOMER_FROM_TAX_ID
CREATE INDEX ON customer_account (ca_c_id);                                         -- CUSTOMER_FROM_TAX_ID
--- Needed for Market-Feed.
CREATE INDEX ON trade_request (tr_s_symb) STORING (tr_tt_id, tr_qty, tr_bid_price) WHERE tr_tt_id IN ('TSL', 'TLS', 'TLB'); -- REQUEST_LIST_SQL
--- Needed for Market-Watch.
CREATE INDEX ON watch_list (wl_c_id);                                               -- PROSPECTIVE_WATCH_STOCK_LIST
CREATE UNIQUE INDEX ON industry (in_name);                                          -- INDUSTRY_WATCH_STOCK_LIST
CREATE INDEX ON company (co_in_id);                                                 -- INDUSTRY_WATCH_STOCK_LIST
--- Needed for Security-Detail.
CREATE UNIQUE INDEX ON daily_market (dm_s_symb, dm_date) STORING (dm_close, dm_high, dm_low, dm_vol); -- MARKET_DETAILS_FOR_SECURITY
CREATE INDEX ON news_xref (nx_co_id);                                                                 -- COMPANY_NEWS_FULL and COMPANY_NEWS_SUMMARIZED
--- Needed for Trade-Lookup.
CREATE UNIQUE INDEX ON holding_history (hh_t_id, hh_h_t_id);                        -- HOLDING_HISTORY_FOR_TRADE
--- Needed for Trade-Order.
CREATE UNIQUE INDEX ON company (co_name);                                           -- SECURITY_FROM_COMPANY_AND_ISSUE_SQL
CREATE UNIQUE INDEX ON security (s_co_id, s_issue) STORING (s_ex_id, s_name);       -- SECURITY_FROM_COMPANY_AND_ISSUE
CREATE INDEX ON customer_taxrate (cx_c_id);                                         -- TAX_RATE_FROM_CUSTOMER_ID_SQL
CREATE UNIQUE INDEX ON holding_summary (hs_s_symb, hs_ca_id) STORING (hs_qty);      -- CUSTOMER_ACCOUNT_BALANCE_AND_ASSETS_SQL
CREATE INDEX ON holding (h_ca_id, h_s_symb, h_dts) STORING (h_qty, h_price);        -- LIFO_HOLDING_LIST and FIFO_HOLDING_LIST
--- Needed for Trade-Status.
CREATE INDEX ON trade (t_ca_id, t_dts DESC) STORING (t_st_id, t_tt_id, t_is_cash, t_s_symb, t_qty, t_bid_price, t_exec_name, t_trade_price, t_chrg); -- TOP_50_TRADES_FOR_ACCOUNT (and Customer-Position::CUSTOMER_TRADE_HISTORY and Trade-Update::TRADES_FOR_CUSTOMER)
--- Needed for Trade-Update.
CREATE INDEX ON trade (t_s_symb, t_dts ASC) STORING (t_ca_id, t_exec_name, t_is_cash, t_trade_price, t_qty, t_tt_id); -- TRADES_FOR_SECURITY_SQL (and Trade-Lookup::TRADES_FOR_SECURITY_SQL)
-- Install Foreign Keys.
ALTER TABLE account_permission ADD FOREIGN KEY (ap_ca_id) REFERENCES customer_account;
ALTER TABLE customer ADD FOREIGN KEY (c_st_id) REFERENCES status_type;
ALTER TABLE customer ADD FOREIGN KEY (c_ad_id) REFERENCES address;
ALTER TABLE customer_account ADD FOREIGN KEY (ca_b_id) REFERENCES broker;
ALTER TABLE customer_account ADD FOREIGN KEY (ca_c_id) REFERENCES customer;
ALTER TABLE customer_taxrate ADD FOREIGN KEY (cx_tx_id) REFERENCES taxrate;
ALTER TABLE customer_taxrate ADD FOREIGN KEY (cx_c_id) REFERENCES customer;
ALTER TABLE holding ADD FOREIGN KEY (h_t_id) REFERENCES trade;
ALTER TABLE holding ADD FOREIGN KEY (h_ca_id, h_s_symb) REFERENCES holding_summary;
ALTER TABLE holding_history ADD FOREIGN KEY (hh_h_t_id) REFERENCES trade;
ALTER TABLE holding_history ADD FOREIGN KEY (hh_t_id) REFERENCES trade;
ALTER TABLE holding_summary ADD FOREIGN KEY (hs_ca_id) REFERENCES customer_account;
ALTER TABLE holding_summary ADD FOREIGN KEY (hs_s_symb) REFERENCES security;
ALTER TABLE watch_item ADD FOREIGN KEY (wi_wl_id) REFERENCES watch_list;
ALTER TABLE watch_item ADD FOREIGN KEY (wi_s_symb) REFERENCES security;
ALTER TABLE watch_list ADD FOREIGN KEY (wl_c_id) REFERENCES customer;
ALTER TABLE broker ADD FOREIGN KEY (b_st_id) REFERENCES status_type;
ALTER TABLE cash_transaction ADD FOREIGN KEY (ct_t_id) REFERENCES trade;
ALTER TABLE charge ADD FOREIGN KEY (ch_tt_id) REFERENCES trade_type;
ALTER TABLE commission_rate ADD FOREIGN KEY (cr_tt_id) REFERENCES trade_type;
ALTER TABLE commission_rate ADD FOREIGN KEY (cr_ex_id) REFERENCES exchange;
ALTER TABLE settlement ADD FOREIGN KEY (se_t_id) REFERENCES trade;
ALTER TABLE trade ADD FOREIGN KEY (t_st_id) REFERENCES status_type;
ALTER TABLE trade ADD FOREIGN KEY (t_tt_id) REFERENCES trade_type;
ALTER TABLE trade ADD FOREIGN KEY (t_s_symb) REFERENCES security;
ALTER TABLE trade ADD FOREIGN KEY (t_ca_id) REFERENCES customer_account;
ALTER TABLE trade_history ADD FOREIGN KEY (th_t_id) REFERENCES trade;
ALTER TABLE trade_history ADD FOREIGN KEY (th_st_id) REFERENCES status_type;
ALTER TABLE trade_request ADD FOREIGN KEY (tr_t_id) REFERENCES trade;
ALTER TABLE trade_request ADD FOREIGN KEY (tr_tt_id) REFERENCES trade_type;
ALTER TABLE trade_request ADD FOREIGN KEY (tr_s_symb) REFERENCES security;
ALTER TABLE trade_request ADD FOREIGN KEY (tr_b_id) REFERENCES broker;
ALTER TABLE company ADD FOREIGN KEY (co_st_id) REFERENCES status_type;
ALTER TABLE company ADD FOREIGN KEY (co_in_id) REFERENCES industry;
ALTER TABLE company ADD FOREIGN KEY (co_ad_id) REFERENCES address;
ALTER TABLE company_competitor ADD FOREIGN KEY (cp_co_id) REFERENCES company;
ALTER TABLE company_competitor ADD FOREIGN KEY (cp_comp_co_id) REFERENCES company;
ALTER TABLE company_competitor ADD FOREIGN KEY (cp_in_id) REFERENCES industry;
ALTER TABLE daily_market ADD FOREIGN KEY (dm_s_symb) REFERENCES security;
ALTER TABLE exchange ADD FOREIGN KEY (ex_ad_id) REFERENCES address;
ALTER TABLE financial ADD FOREIGN KEY (fi_co_id) REFERENCES company;
ALTER TABLE industry ADD FOREIGN KEY (in_sc_id) REFERENCES sector;
ALTER TABLE last_trade ADD FOREIGN KEY (lt_s_symb) REFERENCES security;
ALTER TABLE news_xref ADD FOREIGN KEY (nx_ni_id) REFERENCES news_item;
ALTER TABLE news_xref ADD FOREIGN KEY (nx_co_id) REFERENCES company;
ALTER TABLE security ADD FOREIGN KEY (s_st_id) REFERENCES status_type;
ALTER TABLE security ADD FOREIGN KEY (s_ex_id) REFERENCES exchange;
ALTER TABLE security ADD FOREIGN KEY (s_co_id) REFERENCES company;
ALTER TABLE address ADD FOREIGN KEY (ad_zc_code) REFERENCES zip_code;
-- Install Comments.
COMMENT ON TABLE account_permission IS 'The table contains information about the access the customer or an individual other than the customer has to a given customer account. Customer accounts may have trades executed on them by more than one person.';
COMMENT ON COLUMN account_permission.ap_ca_id IS 'Customer account identifier.';
COMMENT ON COLUMN account_permission.ap_acl IS 'Access Control List defining the permissions the person has on the customer account.';
COMMENT ON COLUMN account_permission.ap_tax_id IS 'Tax identifier of the person with access to the customer account.';
COMMENT ON COLUMN account_permission.ap_l_name IS 'Last name of the person with access to the customer account.';
COMMENT ON COLUMN account_permission.ap_f_name IS 'First name of the person with access to the customer account.';
COMMENT ON TABLE customer IS 'The table contains information about the customers of the brokerage firm.';
COMMENT ON COLUMN customer.c_id IS 'Customer identifier, used internally to link customer information.';
COMMENT ON COLUMN customer.c_tax_id IS e'Customer\'s tax identifier, used externally on communication to the customer. Is alphanumeric.';
COMMENT ON COLUMN customer.c_st_id IS 'Customer status type identifier. Identifies if this customer is active or not.';
COMMENT ON COLUMN customer.c_l_name IS e'Primary Customer\'s last name.';
COMMENT ON COLUMN customer.c_f_name IS e'Primary Customer\'s first name.';
COMMENT ON COLUMN customer.c_m_name IS e'Primary Customer\'s middle name initial.';
COMMENT ON COLUMN customer.c_gndr IS e'Gender of the primary customer. Valid values \'M\' for male or \'F\' for Female.';
COMMENT ON COLUMN customer.c_tier IS 'Customer tier: tier 1 accounts are charged highest fees, tier 2 accounts are charged medium fees, and tier 3 accounts have the lowest fees.';
COMMENT ON COLUMN customer.c_dob IS e'Customer\'s date of birth.';
COMMENT ON COLUMN customer.c_ad_id IS e'Address identifier of the customer\'s address.';
COMMENT ON COLUMN customer.c_ctry_1 IS e'Country code for Customer\'s phone 1.';
COMMENT ON COLUMN customer.c_area_1 IS e'Area code for customer\'s phone 1.';
COMMENT ON COLUMN customer.c_local_1 IS e'Local number for customer\'s phone 1.';
COMMENT ON COLUMN customer.c_ext_1 IS e'Extension number for Customer\'s phone 1.';
COMMENT ON COLUMN customer.c_ctry_2 IS e'Country code for Customer\'s phone 2.';
COMMENT ON COLUMN customer.c_area_2 IS e'Area code for Customer\'s phone 2.';
COMMENT ON COLUMN customer.c_local_2 IS e'Local number for Customer\'s phone 2.';
COMMENT ON COLUMN customer.c_ext_2 IS e'Extension number for Customer\'s phone 2.';
COMMENT ON COLUMN customer.c_ctry_3 IS e'Country code for Customer\'s phone 3.';
COMMENT ON COLUMN customer.c_area_3 IS e'Area code for Customer\'s phone 3.';
COMMENT ON COLUMN customer.c_local_3 IS e'Local number for Customer\'s phone 3.';
COMMENT ON COLUMN customer.c_ext_3 IS e'Extension number for Customer\'s phone 3.';
COMMENT ON COLUMN customer.c_email_1 IS e'Customer\'s e-mail address 1.';
COMMENT ON COLUMN customer.c_email_2 IS e'Customer\'s e-mail address 2.';
COMMENT ON TABLE customer_account IS 'The table contains account information related to accounts of each customer.';
COMMENT ON COLUMN customer_account.ca_id IS 'Customer account identifier.';
COMMENT ON COLUMN customer_account.ca_b_id IS 'Broker identifier of the broker who manages this customer account.';
COMMENT ON COLUMN customer_account.ca_c_id IS 'Customer identifier of the customer who owns this account.';
COMMENT ON COLUMN customer_account.ca_name IS 'Name of customer account. Example, "Trish Hogan 401(k)".';
COMMENT ON COLUMN customer_account.ca_tax_st IS 'Tax status of this account: 0 means this account is not taxable, 1 means this account is taxable and tax must be withheld, 2 means this account is taxable and tax does not have to be withheld.';
COMMENT ON COLUMN customer_account.ca_bal IS e'Account\'s cash balance.';
COMMENT ON TABLE customer_taxrate IS 'The table contains two references per customer into the TAXRATE table. One reference is for state/province tax; the other one is for national tax. The TAXRATE table contains the actual tax rates.';
COMMENT ON COLUMN customer_taxrate.cx_tx_id IS 'Tax rate identifier.';
COMMENT ON COLUMN customer_taxrate.cx_c_id IS 'Customer identifier of a customer that must pay this tax rate.';
COMMENT ON TABLE holding IS e'The table contains information about the customer account\'s security holdings.';
COMMENT ON COLUMN holding.h_t_id IS 'Trade Identifier of the trade.';
COMMENT ON COLUMN holding.h_ca_id IS 'Customer account identifier.';
COMMENT ON COLUMN holding.h_s_symb IS 'Symbol for the security held.';
COMMENT ON COLUMN holding.h_dts IS 'Date this security was purchased or sold.';
COMMENT ON COLUMN holding.h_price IS 'Unit purchase price of this security.';
COMMENT ON COLUMN holding.h_qty IS 'Quantity of this security held.';
COMMENT ON TABLE holding_history IS 'The table contains information about holding positions that were inserted, updated or deleted and which trades made each change.';
COMMENT ON COLUMN holding_history.hh_h_t_id IS 'Trade Identifier of the trade that originally created the holding row. This is a Foreign Key to the TRADE table rather then the HOLDING table because the HOLDING row could be deleted.';
COMMENT ON COLUMN holding_history.hh_t_id IS 'Trade Identifier of the current trade (the one that last inserted, updated or deleted the holding identified by HH_H_T_ID).';
COMMENT ON COLUMN holding_history.hh_before_qty IS 'Quantity of this security held before the modifying trade. On initial insertion, HH_BEFORE_QTY is 0.';
COMMENT ON COLUMN holding_history.hh_after_qty IS 'Quantity of this security held after the modifying trade. If the HOLDING row gets deleted by the modifying trade, then HH_AFTER_QTY is 0.';
COMMENT ON TABLE holding_summary IS e'The table contains aggregate information about the customer account\'s security holdings. This may be implemented as a materialized view on the holding table.';
COMMENT ON COLUMN holding_summary.hs_ca_id IS 'Customer account identifier.';
COMMENT ON COLUMN holding_summary.hs_s_symb IS 'Symbol for the security held.';
COMMENT ON COLUMN holding_summary.hs_qty IS 'Total quantity of this security held.';
COMMENT ON TABLE watch_item IS 'The table contains list of securities to watch for a watch list.';
COMMENT ON COLUMN watch_item.wi_wl_id IS 'Watch list identifier.';
COMMENT ON COLUMN watch_item.wi_s_symb IS 'Symbol of the security to watch.';
COMMENT ON TABLE watch_list IS 'The table contains information about the customer who created this watch list.';
COMMENT ON COLUMN watch_list.wl_id IS 'Watch list identifier.';
COMMENT ON COLUMN watch_list.wl_c_id IS 'Identifier of customer who created this watch list.';
COMMENT ON TABLE broker IS 'The table contains information about brokers.';
COMMENT ON COLUMN broker.b_id IS 'Broker identifier.';
COMMENT ON COLUMN broker.b_st_id IS 'Broker status type identifier; identifies if this broker is active or not.';
COMMENT ON COLUMN broker.b_name IS e'Broker\'s name.';
COMMENT ON COLUMN broker.b_num_trades IS 'Number of trades this broker has executed so far.';
COMMENT ON COLUMN broker.b_comm_total IS 'Amount of commission this broker has earned so far.';
COMMENT ON TABLE cash_transaction IS 'The table contains information about cash transactions.';
COMMENT ON COLUMN cash_transaction.ct_t_id IS 'Trade identifier.';
COMMENT ON COLUMN cash_transaction.ct_dts IS 'Date and time stamp of when the transaction took place.';
COMMENT ON COLUMN cash_transaction.ct_amt IS 'Amount of the cash transaction.';
COMMENT ON COLUMN cash_transaction.ct_name IS 'Transaction name, or description: e.g. "Buy Keebler Cookies", "Cash from sale of DuPont stock".';
COMMENT ON TABLE charge IS e'The table contains information about charges for placing a trade request. Charges are based on the customer\'s tier and the trade type.';
COMMENT ON COLUMN charge.ch_tt_id IS 'Trade type identifier.';
COMMENT ON COLUMN charge.ch_c_tier IS e'Customer\'s tier.';
COMMENT ON COLUMN charge.ch_chrg IS 'Charge for placing a trade request.';
COMMENT ON TABLE commission_rate IS 'The commission rate depends on several factors: the tier the customer is in, the type of trade, the quantity of securities traded, and the exchange that executes the trade.';
COMMENT ON COLUMN commission_rate.cr_c_tier IS e'Customer\'s tier. Valid values 1, 2 or 3.';
COMMENT ON COLUMN commission_rate.cr_tt_id IS 'Trade Type identifier. Identifies the type of trade.';
COMMENT ON COLUMN commission_rate.cr_ex_id IS 'Exchange identifier. Identifies the exchange the trade is against.';
COMMENT ON COLUMN commission_rate.cr_from_qty IS 'Lower bound of quantity being traded to match this commission rate.';
COMMENT ON COLUMN commission_rate.cr_to_qty IS 'Upper bound of quantity being traded to match this commission rate.';
COMMENT ON COLUMN commission_rate.cr_rate IS 'Commission rate. Ranges from 0.00 to 100.00. Example: 10% is 10.00.';
COMMENT ON TABLE settlement IS 'The table contains information about how trades are settled: specifically whether the settlement is on margin or in cash and when the settlement is due.';
COMMENT ON COLUMN settlement.se_t_id IS 'Trade identifier.';
COMMENT ON COLUMN settlement.se_cash_type IS 'Type of cash settlement involved: possible values "Margin", "Cash Account".';
COMMENT ON COLUMN settlement.se_cash_due_date IS 'Date by which customer or brokerage must receive the cash; date of trade plus two days.';
COMMENT ON COLUMN settlement.se_amt IS 'Cash amount of settlement.';
COMMENT ON TABLE trade IS 'The table contains information about trades.';
COMMENT ON COLUMN trade.t_id IS 'Trade identifier.';
COMMENT ON COLUMN trade.t_dts IS 'Date and time of trade.';
COMMENT ON COLUMN trade.t_st_id IS 'Status type identifier; identifies the status of this trade.';
COMMENT ON COLUMN trade.t_tt_id IS 'Trade type identifier; identifies the type of his trade.';
COMMENT ON COLUMN trade.t_is_cash IS 'Is this trade a cash (1) or margin (0) trade?';
COMMENT ON COLUMN trade.t_s_symb IS 'Security symbol of the security that was traded.';
COMMENT ON COLUMN trade.t_qty IS 'Quantity of securities traded.';
COMMENT ON COLUMN trade.t_bid_price IS 'The requested unit price.';
COMMENT ON COLUMN trade.t_ca_id IS 'Customer account identifier.';
COMMENT ON COLUMN trade.t_exec_name IS 'Name of the person executing the trade.';
COMMENT ON COLUMN trade.t_trade_price IS 'Unit price at which the security was traded.';
COMMENT ON COLUMN trade.t_chrg IS 'Fee charged for placing this trade request.';
COMMENT ON COLUMN trade.t_comm IS 'Commission earned on this trade; may be zero.';
COMMENT ON COLUMN trade.t_tax IS 'Amount of tax due on this trade; can be zero. Whether the tax is withheld from the settlement amount depends on the customer account tax status.';
COMMENT ON COLUMN trade.t_lifo IS 'If this trade is closing an existing position, is it executed against the newest-to-oldest account holdings of this security (1=LIFO) or against the oldest-to-newest account holdings (0=FIFO).';
COMMENT ON TABLE trade_history IS 'The table contains the history of each trade transaction through the various states.';
COMMENT ON COLUMN trade_history.th_t_id IS 'Trade identifier. This value will be used for the corresponding T_ID in the TRADE and SE_T_ID in the SETTLEMENT table if this trade request results in a trade.';
COMMENT ON COLUMN trade_history.th_dts IS 'Timestamp of when the trade history was updated.';
COMMENT ON COLUMN trade_history.th_st_id IS 'Status type identifier.';
COMMENT ON TABLE trade_request IS 'The table contains information about pending limit trades that are waiting for a certain security price before the trades are submitted to the market.';
COMMENT ON COLUMN trade_request.tr_t_id IS 'Trade request identifier. This value will be used for processing the pending limit order when it is subsequently triggered.';
COMMENT ON COLUMN trade_request.tr_tt_id IS 'Trade request type identifier; identifies the type of trade.';
COMMENT ON COLUMN trade_request.tr_s_symb IS 'Security symbol of the security the customer wants to trade.';
COMMENT ON COLUMN trade_request.tr_qty IS 'Quantity of security the customer had requested to trade.';
COMMENT ON COLUMN trade_request.tr_bid_price IS 'Price the customer wants per unit of security that they want to trade. Value of zero implies the customer wants to trade now at the market price.';
COMMENT ON COLUMN trade_request.tr_b_id IS 'Identifies the broker handling the trade.';
COMMENT ON TABLE trade_type IS 'The table contains a list of valid trade types.';
COMMENT ON COLUMN trade_type.tt_id IS 'Trade type identifier: Values are: "TMB", "TMS", "TSL", "TLS", and "TLB".';
COMMENT ON COLUMN trade_type.tt_name IS 'Trade type name. Examples "Limit Buy", "Limit Sell", "Market Buy", "Market Sell", "Stop Loss".';
COMMENT ON COLUMN trade_type.tt_is_sell IS '1 if this is a "Sell" type transaction. 0 if this is a "Buy" type transaction.';
COMMENT ON COLUMN trade_type.tt_is_mrkt IS '1 if this is a market transaction that is submitted to the market exchange emulator immediately. 0 if this is a limit transaction.';
COMMENT ON TABLE company IS 'The table contains information about all companies with publicly traded securities.';
COMMENT ON COLUMN company.co_id IS 'Company identifier.';
COMMENT ON COLUMN company.co_st_id IS 'Company status type identifier. Identifies if this company is active or not.';
COMMENT ON COLUMN company.co_name IS 'Company name.';
COMMENT ON COLUMN company.co_in_id IS 'Industry identifier of the industry the company is in.';
COMMENT ON COLUMN company.co_sp_rate IS e'Company\'s credit rating from Standard & Poor.';
COMMENT ON COLUMN company.co_ceo IS e'Name of Company\'s Chief Executive Officer.';
COMMENT ON COLUMN company.co_ad_id IS 'Address identifier.';
COMMENT ON COLUMN company.co_desc IS 'Company description.';
COMMENT ON COLUMN company.co_open_date IS 'Date the company was founded.';
COMMENT ON TABLE company_competitor IS 'The table contains information for the competitors of a given company and the industry in which the company competes.';
COMMENT ON COLUMN company_competitor.cp_co_id IS 'Company identifier.';
COMMENT ON COLUMN company_competitor.cp_comp_co_id IS 'Company identifier of the competitor company for the specified industry.';
COMMENT ON COLUMN company_competitor.cp_in_id IS e'Industry identifier of the industry in which the CP_CO_ID company considers that the CP_COMP_CO_ID company competes with it. This may not be either company\'s primary industry.';
COMMENT ON TABLE daily_market IS 'The table contains daily market statistics for each security, using the closing market data from the last completed trading day. EGenLoader will load this table with data for each security for the period starting 3 January 2000 and ending 31 December 2004.';
COMMENT ON COLUMN daily_market.dm_date IS 'Date of last completed trading day.';
COMMENT ON COLUMN daily_market.dm_s_symb IS 'Security symbol of this security.';
COMMENT ON COLUMN daily_market.dm_close IS 'Closing price for this security.';
COMMENT ON COLUMN daily_market.dm_high IS e' Day\'s High price for this security.';
COMMENT ON COLUMN daily_market.dm_low IS e'Day\'s Low price for this security.';
COMMENT ON COLUMN daily_market.dm_vol IS e'Day\'s volume for this security.';
COMMENT ON TABLE exchange IS 'The table contains information about financial exchanges.';
COMMENT ON COLUMN exchange.ex_id IS 'Exchange identifier. Values are, "NYSE", "NASDAQ", "AMEX", "PCX".';
COMMENT ON COLUMN exchange.ex_name IS 'Exchange name.';
COMMENT ON COLUMN exchange.ex_num_symb IS 'Number of securities traded on this exchange.';
COMMENT ON COLUMN exchange.ex_open IS 'Exchange Daily start time expressed in GMT.';
COMMENT ON COLUMN exchange.ex_close IS 'Exchange Daily stop time, expressed in GMT.';
COMMENT ON COLUMN exchange.ex_desc IS 'Description of the exchange.';
COMMENT ON COLUMN exchange.ex_ad_id IS 'Mailing address of exchange.';
COMMENT ON TABLE financial IS e'The table contains information about a company\'s quarterly financial reports. EGenLoader will load this table with financial information for each company for the Quarters starting 1 January 2000 and ending with the quarter that starts 1 October 2004.';
COMMENT ON COLUMN financial.fi_co_id IS 'Company identifier.';
COMMENT ON COLUMN financial.fi_year IS 'Year of the quarter end.';
COMMENT ON COLUMN financial.fi_qtr IS 'Quarter number that the financial information is for: valid values 1, 2, 3, 4.';
COMMENT ON COLUMN financial.fi_qtr_start_date IS 'Start date of quarter.';
COMMENT ON COLUMN financial.fi_revenue IS 'Reported revenue for the quarter.';
COMMENT ON COLUMN financial.fi_net_earn IS 'Net earnings reported for the quarter.';
COMMENT ON COLUMN financial.fi_basic_eps IS 'Basic earnings per share reported for the quarter.';
COMMENT ON COLUMN financial.fi_dilut_eps IS 'Diluted earnings per share reported for the quarter.';
COMMENT ON COLUMN financial.fi_margin IS 'Profit divided by revenues for the quarter.';
COMMENT ON COLUMN financial.fi_inventory IS 'Value of inventory on hand at the end of the quarter.';
COMMENT ON COLUMN financial.fi_assets IS 'Value of total assets at the end of the quarter.';
COMMENT ON COLUMN financial.fi_liability IS 'Value of total liabilities at the end of the quarter.';
COMMENT ON COLUMN financial.fi_out_basic IS 'Average number of common shares outstanding (basic).';
COMMENT ON COLUMN financial.fi_out_dilut IS 'Average number of common shares outstanding (diluted).';
COMMENT ON TABLE industry IS 'The table contains information about industries. Used to categorize which industries a company is in.';
COMMENT ON COLUMN industry.in_id IS 'Industry identifier.';
COMMENT ON COLUMN industry.in_name IS 'Industry name. Examples: "Air Travel", "Air Cargo", "Software", "Consumer Banking", "Merchant Banking", etc.';
COMMENT ON COLUMN industry.in_sc_id IS 'Sector identifier of the sector the industry is in.';
COMMENT ON TABLE last_trade IS 'The table contains one row for each security with the latest trade price and volume for each security.';
COMMENT ON COLUMN last_trade.lt_s_symb IS 'Security symbol.';
COMMENT ON COLUMN last_trade.lt_dts IS 'Date and timestamp of when this row was last updated.';
COMMENT ON COLUMN last_trade.lt_price IS 'Latest trade price for this security.';
COMMENT ON COLUMN last_trade.lt_open_price IS 'Price the security opened at today.';
COMMENT ON COLUMN last_trade.lt_vol IS 'Volume of trading on the market for this security so far today. Value initialized to 0.';
COMMENT ON TABLE news_item IS 'The table contains information about news items of interest.';
COMMENT ON COLUMN news_item.ni_id IS 'News item identifier.';
COMMENT ON COLUMN news_item.ni_headline IS 'News item headline.';
COMMENT ON COLUMN news_item.ni_summary IS 'News item summary.';
COMMENT ON COLUMN news_item.ni_item IS 'Large object containing the news item or links to the story.';
COMMENT ON COLUMN news_item.ni_dts IS 'Date and time the news item was published.';
COMMENT ON COLUMN news_item.ni_source IS 'Source of the news item.';
COMMENT ON COLUMN news_item.ni_author IS 'Author of the news item. May be null if the news item came off a wire service.';
COMMENT ON TABLE news_xref IS 'The table contains a cross-reference of news items to companies that are mentioned in the news item.';
COMMENT ON COLUMN news_xref.nx_ni_id IS 'News item identifier.';
COMMENT ON COLUMN news_xref.nx_co_id IS 'Company identifier of the company (or one of the companies) mentioned in the news item.';
COMMENT ON TABLE sector IS 'The table contains information about market sectors.';
COMMENT ON COLUMN sector.sc_id IS 'Sector identifier.';
COMMENT ON COLUMN sector.sc_name IS 'Sector name. Examples: "Energy", "Materials", "Industrials", "Health Care", etc.';
COMMENT ON TABLE security IS 'The table contains information about each security traded on any of the exchanges.';
COMMENT ON COLUMN security.s_symb IS 'Security symbol used to identify the security on "ticker".';
COMMENT ON COLUMN security.s_issue IS 'Security issue type. Example: "COMMON", "PERF_A", "PERF_B", etc.';
COMMENT ON COLUMN security.s_st_id IS 'Security status type identifier. Identifies if this security is active or not.';
COMMENT ON COLUMN security.s_name IS 'Security name.';
COMMENT ON COLUMN security.s_ex_id IS 'Exchange identifier of the exchange the security is traded on.';
COMMENT ON COLUMN security.s_co_id IS 'Company identifier of the company this security is issued by.';
COMMENT ON COLUMN security.s_num_out IS 'Number of shares outstanding for this security.';
COMMENT ON COLUMN security.s_start_date IS 'Date security first started trading.';
COMMENT ON COLUMN security.s_exch_date IS 'Date security first started trading on this exchange.';
COMMENT ON COLUMN security.s_pe IS 'Current share price to earnings per share ratio.';
COMMENT ON COLUMN security.s_52wk_high IS 'Security share price 52-week high.';
COMMENT ON COLUMN security.s_52wk_high_date IS 'Date of security share price 52-week high.';
COMMENT ON COLUMN security.s_52wk_low IS 'Security share price 52-week low.';
COMMENT ON COLUMN security.s_52wk_low_date IS 'Date of security share price 52-week low.';
COMMENT ON COLUMN security.s_dividend IS 'Annual Dividend per share amount. May be zero, is not allowed to be negative.';
COMMENT ON COLUMN security.s_yield IS 'Dividend to share price ratio. Value is in percent. Example 10.00 is 10%.';
COMMENT ON TABLE address IS 'The table contains address information.';
COMMENT ON COLUMN address.ad_id IS 'Address identifier.';
COMMENT ON COLUMN address.ad_line1 IS 'Address Line 1.';
COMMENT ON COLUMN address.ad_line2 IS 'Address Line 2.';
COMMENT ON COLUMN address.ad_zc_code IS 'Zip or postal code.';
COMMENT ON COLUMN address.ad_ctry IS 'Country.';
COMMENT ON TABLE status_type IS 'The table contains all status values for several different status usages. Multiple tables reference this table to obtain their status values.';
COMMENT ON COLUMN status_type.st_id IS 'Status type identifier.';
COMMENT ON COLUMN status_type.st_name IS 'Status value. Examples: "Active", "Completed", "Pending", "Canceled" and "Submitted".';
COMMENT ON TABLE taxrate IS 'The table contains information about tax rates.';
COMMENT ON COLUMN taxrate.tx_id IS e'Tax rate identifier. Format - two letters followed by one digit. Examples: \'US1\', \'CA1\'.';
COMMENT ON COLUMN taxrate.tx_name IS 'Tax rate name.';
COMMENT ON COLUMN taxrate.tx_rate IS 'Tax rate, between 0.00000 and 1.00000, inclusive.';
COMMENT ON TABLE zip_code IS 'The table contains zip and postal codes, towns, and divisions that go with them.';
COMMENT ON COLUMN zip_code.zc_code IS 'Postal code.';
COMMENT ON COLUMN zip_code.zc_town IS 'Town.';
COMMENT ON COLUMN zip_code.zc_div IS 'State or province or county.';`

// gs://cockroach-fixtures/tpce-csv/customers=5000/*/Trade.txt
var importStmts = []string{
	// Fixed-size tables.
	"IMPORT INTO charge CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/Charge.txt') WITH delimiter = '|';",
	"IMPORT INTO commission_rate CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/CommissionRate.txt') WITH delimiter = '|';",
	"IMPORT INTO trade_type CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/TradeType.txt') WITH delimiter = '|';",
	"IMPORT INTO exchange CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/Exchange.txt') WITH delimiter = '|';",
	"IMPORT INTO industry CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/Industry.txt') WITH delimiter = '|';",
	"IMPORT INTO sector CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/Sector.txt') WITH delimiter = '|';",
	"IMPORT INTO status_type CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/StatusType.txt') WITH delimiter = '|';",
	"IMPORT INTO taxrate CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/TaxRate.txt') WITH delimiter = '|';",
	"IMPORT INTO zip_code CSV DATA (' gs://cockroach-fixtures/tpce-csv/fixed/ZipCode.txt') WITH delimiter = '|';",
	// Scaling tables. Chunked.
	"IMPORT INTO account_permission CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/AccountPermission.txt') WITH delimiter = '|';",
	"IMPORT INTO customer CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Customer.txt') WITH delimiter = '|';",
	"IMPORT INTO customer_account CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/CustomerAccount.txt') WITH delimiter = '|';",
	"IMPORT INTO customer_taxrate CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/CustomerTaxrate.txt') WITH delimiter = '|';",
	"IMPORT INTO holding CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Holding.txt') WITH delimiter = '|';",
	"IMPORT INTO holding_history CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/HoldingHistory.txt') WITH delimiter = '|';",
	"IMPORT INTO holding_summary CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/HoldingSummary.txt') WITH delimiter = '|';",
	"IMPORT INTO watch_item CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/WatchItem.txt') WITH delimiter = '|';",
	"IMPORT INTO watch_list CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/WatchList.txt') WITH delimiter = '|';",
	"IMPORT INTO broker CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Broker.txt') WITH delimiter = '|';",
	"IMPORT INTO cash_transaction CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/CashTransaction.txt') WITH delimiter = '|';",
	"IMPORT INTO settlement CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Settlement.txt') WITH delimiter = '|';",
	"IMPORT INTO trade CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Trade.txt') WITH delimiter = '|';",
	"IMPORT INTO trade_history CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/TradeHistory.txt') WITH delimiter = '|';",
	"IMPORT INTO company CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Company.txt') WITH delimiter = '|';",
	"IMPORT INTO company_competitor CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/CompanyCompetitor.txt') WITH delimiter = '|';",
	"IMPORT INTO daily_market CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/DailyMarket.txt') WITH delimiter = '|';",
	"IMPORT INTO financial CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Financial.txt') WITH delimiter = '|';",
	"IMPORT INTO last_trade CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/LastTrade.txt') WITH delimiter = '|';",
	"IMPORT INTO news_item CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/NewsItem.txt') WITH delimiter = '|';",
	"IMPORT INTO news_xref CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/NewsXRef.txt') WITH delimiter = '|';",
	"IMPORT INTO security CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Security.txt') WITH delimiter = '|';",
	"IMPORT INTO address CSV DATA (' gs://cockroach-fixtures/tpce-csv/customers=500/*/Address.txt') WITH delimiter = '|';",
}
