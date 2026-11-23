-- KilatMart inventory service: stock database (MySQL).
-- Runs once, on the first start of the stock-db container, inside database `kilatmart`.
-- Note the different naming from the orders DB (product_code vs sku):
-- the federated query has to reconcile it, just like in a real company.

CREATE TABLE stock (
    product_code  VARCHAR(20)  NOT NULL PRIMARY KEY,
    product_name  VARCHAR(100) NOT NULL,
    warehouse     VARCHAR(50)  NOT NULL,
    qty_on_hand   INT          NOT NULL,
    reorder_level INT          NOT NULL,
    updated_at    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

INSERT INTO stock (product_code, product_name, warehouse, qty_on_hand, reorder_level) VALUES
    ('KM-001', 'Kopi Gayo Arabika 250 g', 'Cikarang', 40, 15),
    ('KM-002', 'Sambal Bawang Jar 200 g', 'Cikarang',  3, 10),  -- already low
    ('KM-003', 'Teh Melati Premium Box',  'Surabaya', 25, 10),
    ('KM-004', 'Keripik Singkong Balado', 'Surabaya',  6,  8);  -- already low

-- Least privilege: the federation engine gets a read-only login.
CREATE USER 'analytics_ro'@'%' IDENTIFIED BY 'analytics_ro';
GRANT SELECT ON kilatmart.* TO 'analytics_ro'@'%';
