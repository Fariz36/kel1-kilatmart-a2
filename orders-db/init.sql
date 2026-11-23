-- KilatMart web shop: orders database (PostgreSQL).
-- Runs once, on the first start of the orders-db container.

CREATE TABLE orders (
    order_id      SERIAL PRIMARY KEY,
    customer_name TEXT          NOT NULL,
    customer_city TEXT          NOT NULL,
    sku           TEXT          NOT NULL,          -- joins stock.product_code in MySQL
    quantity      INTEGER       NOT NULL CHECK (quantity > 0),
    unit_price    NUMERIC(12,2) NOT NULL,          -- IDR
    status        TEXT          NOT NULL
                  CHECK (status IN ('pending', 'paid', 'shipped', 'cancelled')),
    ordered_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
);

INSERT INTO orders (customer_name, customer_city, sku, quantity, unit_price, status, ordered_at) VALUES
    ('Budi Santoso',    'Bandung',    'KM-001', 2, 85000, 'shipped',   '2026-11-20 09:15+07'),
    ('Siti Rahmawati',  'Jakarta',    'KM-002', 3, 32000, 'paid',      '2026-11-21 10:02+07'),
    ('Andi Pratama',    'Surabaya',   'KM-003', 1, 45000, 'pending',   '2026-11-21 13:40+07'),
    ('Dewi Lestari',    'Yogyakarta', 'KM-002', 1, 32000, 'paid',      '2026-11-22 08:30+07'),
    ('Rizky Hidayat',   'Medan',      'KM-004', 4, 18000, 'paid',      '2026-11-22 16:55+07'),
    ('Putri Maharani',  'Bandung',    'KM-003', 2, 45000, 'paid',      '2026-11-23 07:20+07'),
    ('Fajar Nugroho',   'Semarang',   'KM-002', 2, 32000, 'pending',   '2026-11-23 11:05+07'),
    ('Maya Kusuma',     'Denpasar',   'KM-001', 1, 85000, 'cancelled', '2026-11-23 12:45+07');

-- Least privilege: the federation engine gets a read-only login.
CREATE ROLE analytics_ro LOGIN PASSWORD 'analytics_ro';
GRANT USAGE ON SCHEMA public TO analytics_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO analytics_ro;
