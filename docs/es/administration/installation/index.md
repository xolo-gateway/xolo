# Instalación y despliegue

Antes de actualizar una base existente, siga el [procedimiento de migración UUID](./uuid-migration.md). Detenga todas las réplicas y workers antiguos.

Esta pagina aun no ha sido traducida. Consulte la [version en frances](https://xolo-gateway.org/latest/) para el contenido completo.

## Sondas de salud

Cada instancia responde sin autenticación en dos rutas, para un balanceador de
carga o un orquestador:

| Ruta       | Responde `200` cuando…                                       | Uso                      |
| ---------- | ------------------------------------------------------------ | ------------------------ |
| `/healthz` | el proceso atiende peticiones HTTP                           | sonda de vida (liveness) |
| `/readyz`  | la instancia también alcanza su base de datos (en 0,8 s)     | sonda de disponibilidad  |

`/readyz` responde `503` mientras la base de datos es inalcanzable: la
instancia sale del balanceador sin reiniciarse. Su resultado se comparte
durante 0,8 s, de modo que una avalancha de sondas solo cuesta una
comprobación a la base. Ambas rutas se atienden antes de la resolución del
inquilino, del registro, de las métricas HTTP y del limitador de tasa: una
sonda se dirige a la instancia por su propia dirección, que no es el dominio de
ningún inquilino.

En PostgreSQL, `/readyz` usa una conexión propia, fuera del pool de la
aplicación: un pool saturado por el tráfico no saca a todas las instancias del
balanceador a la vez. La sonda indica si la base responde, no si la instancia
está ocupada. Cuente una conexión más por instancia.

```yaml
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:3002/readyz"]
```

## Paquetes RPM y Debian

Cada versión publica en su [página de GitHub](https://github.com/xolo-gateway/xolo/releases) paquetes `.rpm` y `.deb`, para `x86_64` y `arm64`:

| Paquete             | Contenido                                                                                  |
| ------------------- | ------------------------------------------------------------------------------------------ |
| `xolo`              | `xolo-server`, `xolo-migrate`, `xolo-adoption`, `xolo-admin`, el servicio systemd `xolo`   |
| `xolo-plugin-<nombre>` | un plugin oficial, en `/usr/lib/xolo/plugins` (`xolo-plugin-pseudonymizer`, `xolo-plugin-mcp-bridge`…) |
| `xolo-plugin-all`   | ningún archivo: depende de todos los paquetes de plugins oficiales                         |

Los binarios están enlazados estáticamente: los paquetes no dependen de ninguna biblioteca del sistema. Se instalan y verifican en cada cambio en Red Hat Enterprise Linux 10 y 9, Debian 13 y 12; las distribuciones derivadas (AlmaLinux, Rocky Linux, Ubuntu) también sirven.

Descargue el paquete `xolo` y los de los plugins deseados, o todos los paquetes de la versión con `xolo-plugin-all`, e instálelos juntos:

```bash
# Red Hat Enterprise Linux, AlmaLinux, Rocky Linux: todo
sudo dnf install ./xolo-*.x86_64.rpm ./xolo-plugin-all-*.noarch.rpm

# Debian, Ubuntu: el servidor y dos plugins
sudo apt install ./xolo_*_amd64.deb ./xolo-plugin-pseudonymizer_*_amd64.deb ./xolo-plugin-mcp-bridge_*_amd64.deb
```

El servidor solo descubre sus plugins al arrancar: instalar, actualizar o retirar paquetes de plugins reinicia el servicio si está en marcha, una sola vez para todos los paquetes de una misma orden. En Red Hat, el reinicio que sigue a una eliminación llega 5 segundos después de la orden.

Los paquetes no están firmados y ningún repositorio `dnf` o `apt` los sirve: compruebe los archivos descargados con el `checksums.txt` de la versión.

La instalación crea la cuenta de sistema `xolo` y estos archivos:

| Ruta                   | Función                                                                                      |
| ---------------------- | -------------------------------------------------------------------------------------------- |
| `/etc/xolo/xolo.env`   | Configuración: una variable `XOLO_*` por línea. Se conserva en las actualizaciones.          |
| `/etc/xolo/secret.env` | `XOLO_SECRET_KEY` y `XOLO_HTTP_SESSION_KEYS`, generados en la primera instalación, nunca reemplazados. |
| `/var/lib/xolo`        | Base SQLite (`data.sqlite`), creada en el primer arranque.                                   |
| `/var/cache/xolo`      | Modelos descargados por el plugin `pseudonymizer`.                                           |

Guarde `/etc/xolo/secret.env` junto con la base: sin `XOLO_SECRET_KEY`, las claves API de los proveedores que contiene son ilegibles.

El paquete no habilita ni arranca el servicio, que necesita antes un proveedor de identidad. Complete `/etc/xolo/xolo.env` (vea [Configuración](./configuration.md)) y luego:

```bash
sudo systemctl enable --now xolo
sudo journalctl -u xolo -f
```

Una actualización del paquete reinicia el servicio si está en marcha. Su desinstalación lo detiene y deja en su sitio la configuración, `/etc/xolo/secret.env` y la base.

Las órdenes de administración sin conexión `xolo-migrate` ([migración UUID](./uuid-migration.md)) y `xolo-adoption` solo leen su configuración del entorno. `xolo-admin` las ejecuta con la del servicio (`/etc/xolo/xolo.env` y `/etc/xolo/secret.env`), con la cuenta `xolo`: ejecutadas como root, dejarían en la base SQLite archivos que el servicio ya no podría abrir.

```bash
sudo systemctl stop xolo
sudo xolo-admin migrate diagnose
sudo xolo-admin migrate plan -out /var/lib/xolo/recovery.json
```

Los archivos que escriben deben poder ser escritos por la cuenta `xolo`, en `/var/lib/xolo` por ejemplo.

En Red Hat Enterprise Linux, abra el puerto en el cortafuegos si la instancia se alcanza sin proxy inverso en la misma máquina: `sudo firewall-cmd --permanent --add-port=3002/tcp && sudo firewall-cmd --reload`.
