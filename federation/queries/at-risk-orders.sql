-- The analytics team's question (must-show item for A2):
-- "Which PAID orders are for a product that is LOW on stock?"
-- orders live in PostgreSQL (web shop), stock lives in MySQL (inventory).
-- Nothing is copied: DuckDB reads both databases at query time.
SELECT
    o.order_id,
    o.customer_name,
    o.sku,
    s.product_name,
    o.quantity               AS ordered_qty,
    s.qty_on_hand,
    s.reorder_level,
    s.warehouse,
    o.ordered_at
FROM orders_pg.public.orders AS o
JOIN stock_my.stock          AS s ON s.product_code = o.sku
WHERE o.status = 'paid'
  AND s.qty_on_hand < s.reorder_level
ORDER BY o.ordered_at;
