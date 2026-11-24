create table schema_name.test_sample_data
(
    product_code   STRING,
    snapshot_date  DATE,
    stock_quantity INT,
    stock_value DOUBLE,
    sales_quantity INT,
    sales_value DOUBLE
);
insert into schema_name.test_sample_data
values ('PRODUCT_1', '2026-11-01', 12, 1235.45, 3, 45.5),
       ('PRODUCT_1', '2026-11-02', 14, 1235.45, 3, 45.5),
       ('PRODUCT_1', '2026-11-03', 9, 1235.45, 3, 45.5),
       ('PRODUCT_1', '2026-11-04', 11, 1235.45, 5, 75.8),
       ('PRODUCT_1', '2026-11-05', 7, 1235.45, 4, 60.2),
       ('PRODUCT_1', '2026-11-06', 15, 1235.45, 6, 90.0),
       ('PRODUCT_1', '2026-11-07', 10, 1235.45, 2, 30.5),
       ('PRODUCT_2', '2026-11-01', 20, 2450.00, 8, 120.0),
       ('PRODUCT_2', '2026-11-02', 18, 2450.00, 5, 75.0),
       ('PRODUCT_2', '2026-11-03', 25, 2450.00, 10, 150.5),
       ('PRODUCT_2', '2026-11-04', 22, 2450.00, 7, 105.0),
       ('PRODUCT_2', '2026-11-05', 16, 2450.00, 9, 135.0),
       ('PRODUCT_3', '2026-11-01', 5, 899.99, 1, 15.5),
       ('PRODUCT_3', '2026-11-02', 8, 899.99, 2, 31.0),
       ('PRODUCT_3', '2026-11-03', 6, 899.99, 3, 46.5);

create table schema_name.test_sample_data_products
(
    product_code STRING,
    product_name STRING
);
INSERT INTO schema_name.test_sample_data_products
VALUES ('PRODUCT_1', 'Cargo pants'),
       ('PRODUCT_2', 'Gnagsta hoodie'),
       ('PRODUCT_3', 'Balanciaga shoes');