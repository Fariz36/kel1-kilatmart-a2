-- Second report: per product, can the warehouse cover the paid-but-unshipped orders?
SELECT
    s.product_code                                        AS sku,
    s.product_name,
    s.qty_on_hand,
    COALESCE(SUM(o.quantity) FILTER (WHERE o.status = 'paid'), 0)                 AS paid_unshipped_qty,
    s.qty_on_hand - COALESCE(SUM(o.quantity) FILTER (WHERE o.status = 'paid'), 0) AS left_after_fulfilment,
    s.reorder_level
FROM stock_my.stock               AS s
LEFT JOIN orders_pg.public.orders AS o ON o.sku = s.product_code
GROUP BY s.product_code, s.product_name, s.qty_on_hand, s.reorder_level
ORDER BY left_after_fulfilment;
