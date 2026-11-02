// Shared API errors and synchronization status messages for supplier billing.
export const billingErrorResources: Record<string, Record<string, string>> = {
  en: {
    'A reversal reason is required': 'A reversal reason is required',
    'Access denied.': 'Access denied.',
    'Account changed. Sign in again.': 'Account changed. Sign in again.',
    'Amount is out of range': 'Amount is out of range',
    'Authentication is busy. Try again shortly.':
      'Authentication is busy. Try again shortly.',
    'Authentication is unavailable.': 'Authentication is unavailable.',
    'Awaiting sync': 'Awaiting sync',
    'Choose a password that is not commonly used.':
      'Choose a password that is not commonly used.',
    'Choose a valid role and supplier.': 'Choose a valid role and supplier.',
    'Data freshness': 'Data freshness',
    'Database operation failed': 'Database operation failed',
    'Date range must be within 366 days': 'Date range must be within 366 days',
    'Enter unique positive channel IDs': 'Enter unique positive channel IDs',
    'Frontend build is not configured': 'Frontend build is not configured',
    'Idempotency key was already used': 'Idempotency key was already used',
    'In progress': 'In progress',
    'Invalid account.': 'Invalid account.',
    'Invalid date range': 'Invalid date range',
    'Invalid decimal amount': 'Invalid decimal amount',
    'Invalid ledger amount': 'Invalid ledger amount',
    'Invalid recharge': 'Invalid recharge',
    'Invalid report date range': 'Invalid report date range',
    'Invalid report source request': 'Invalid report source request',
    'Invalid report source URL': 'Invalid report source URL',
    'Invalid request': 'Invalid request',
    'Invalid request origin': 'Invalid request origin',
    'Invalid source configuration': 'Invalid source configuration',
    'Invalid source URL': 'Invalid source URL',
    'Invalid supplier': 'Invalid supplier',
    'Invalid usage item': 'Invalid usage item',
    'Invalid username or password.': 'Invalid username or password.',
    'Manual entries only': 'Manual entries only',
    'New password must be different from current password.':
      'New password must be different from current password.',
    'Not found': 'Not found',
    'Password must contain between 15 and 128 characters.':
      'Password must contain between 15 and 128 characters.',
    'Pending days: {{count}}': 'Pending days: {{count}}',
    'Permission denied': 'Permission denied',
    'Please sign in.': 'Please sign in.',
    'Previously billed source rows are missing; sync was not applied':
      'Previously billed source rows are missing; sync was not applied',
    'Recharge not found': 'Recharge not found',
    'Record changed; retry the request': 'Record changed; retry the request',
    'Report billing timezone is unavailable':
      'Report billing timezone is unavailable',
    'Report source credential environment variable is invalid':
      'Report source credential environment variable is invalid',
    'Report source credential is not configured':
      'Report source credential is not configured',
    'Report source must return an array': 'Report source must return an array',
    'Report source request failed': 'Report source request failed',
    'Report source requires HTTPS': 'Report source requires HTTPS',
    'Report source requires positive channel IDs':
      'Report source requires positive channel IDs',
    'Report source response is incomplete or too large':
      'Report source response is incomplete or too large',
    'Report source returned an invalid usage amount':
      'Report source returned an invalid usage amount',
    'Report source returned an invalid usage count':
      'Report source returned an invalid usage count',
    'Report source returned an invalid usage period or model':
      'Report source returned an invalid usage period or model',
    'Report source returned an unsuccessful response':
      'Report source returned an unsuccessful response',
    'Report source returned duplicate usage data':
      'Report source returned duplicate usage data',
    'Report source returned incomplete usage data':
      'Report source returned incomplete usage data',
    'Report source returned invalid usage data':
      'Report source returned invalid usage data',
    'Report source usage identity is invalid':
      'Report source usage identity is invalid',
    'Request verification failed. Refresh and try again.':
      'Request verification failed. Refresh and try again.',
    'Same-currency exchange rate must be 1':
      'Same-currency exchange rate must be 1',
    'Select a supplier': 'Select a supplier',
    'Select between 1 and 500 channels': 'Select between 1 and 500 channels',
    'Service unavailable': 'Service unavailable',
    'Source bindings cannot be changed': 'Source bindings cannot be changed',
    'Source channel belongs to a different supplier head':
      'Source channel belongs to a different supplier head',
    'Source channels are already assigned or could not be saved':
      'Source channels are already assigned or could not be saved',
    'Source not found': 'Source not found',
    'Source origin is not allowed by the server':
      'Source origin is not allowed by the server',
    'Source sync could not be saved': 'Source sync could not be saved',
    'Source URL must use HTTPS': 'Source URL must use HTTPS',
    'Source vendor mapping is invalid': 'Source vendor mapping is invalid',
    'Sources synced': 'Sources synced',
    'Supplier not found': 'Supplier not found',
    'Sync failed': 'Sync failed',
    'Sync range must be within 91 days and not in the future':
      'Sync range must be within 91 days and not in the future',
    'The last billing manager cannot be disabled.':
      'The last billing manager cannot be disabled.',
    "Today's usage may still change.": "Today's usage may still change.",
    'Too many authentication attempts. Try again later.':
      'Too many authentication attempts. Try again later.',
    'Unable to create account. Check the username and try again.':
      'Unable to create account. Check the username and try again.',
    'Unable to disable account. Refresh and try again.':
      'Unable to disable account. Refresh and try again.',
    'Unsynced sources may add expenses to this statement.':
      'Unsynced sources may add expenses to this statement.',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.',
    'Invalid exchange rate mode': 'Invalid exchange rate mode',
    'Invalid initial balance': 'Invalid initial balance',
    'Initial balance changed; reload and retry': 'Initial balance changed; reload and retry',
    'Invalid supplier claim': 'Invalid supplier claim',
    'Invalid usage price': 'Invalid usage price',
    'No imported usage for this line': 'No imported usage for this line',
    'Source mapping overlaps another source': 'Source mapping overlaps another source',
    'Select a remote profile': 'Select a remote profile',
    'Invalid source mapping': 'Invalid source mapping',
    'Select between 1 and 500 mapped values': 'Select between 1 and 500 mapped values',
  },
  zh: {
    'A reversal reason is required': '请填写冲正原因',
    'Access denied.': '无权访问。',
    'Account changed. Sign in again.': '账号已变更，请重新登录。',
    'Amount is out of range': '金额超出允许范围',
    'Authentication is busy. Try again shortly.': '登录服务繁忙，请稍后重试。',
    'Authentication is unavailable.': '登录服务暂不可用。',
    'Awaiting sync': '等待同步',
    'Choose a password that is not commonly used.': '请使用不常见的密码。',
    'Choose a valid role and supplier.': '请选择有效的角色和供应商。',
    'Data freshness': '数据更新状态',
    'Database operation failed': '数据库操作失败',
    'Date range must be within 366 days': '日期范围不能超过 366 天',
    'Enter unique positive channel IDs': '请输入不重复的正整数渠道 ID',
    'Frontend build is not configured': '尚未配置前端构建目录',
    'Idempotency key was already used': '该提交标识已用于其他内容',
    'In progress': '进行中',
    'Invalid account.': '账号无效。',
    'Invalid date range': '日期范围无效',
    'Invalid decimal amount': '金额格式无效',
    'Invalid ledger amount': '账本金额无效',
    'Invalid recharge': '充值信息无效',
    'Invalid report date range': '报表日期范围无效',
    'Invalid report source request': '报表数据源请求无效',
    'Invalid report source URL': '报表数据源地址无效',
    'Invalid request': '请求无效',
    'Invalid request origin': '请求来源无效',
    'Invalid source configuration': '数据源配置无效',
    'Invalid source URL': '数据源地址无效',
    'Invalid supplier': '供应商信息无效',
    'Invalid usage item': '用量项目无效',
    'Invalid username or password.': '用户名或密码错误。',
    'Manual entries only': '仅手动录入',
    'New password must be different from current password.':
      '新密码不能与当前密码相同。',
    'Not found': '未找到',
    'Password must contain between 15 and 128 characters.':
      '密码长度必须为 15 至 128 个字符。',
    'Pending days: {{count}}': '待同步天数：{{count}}',
    'Permission denied': '权限不足',
    'Please sign in.': '请先登录。',
    'Previously billed source rows are missing; sync was not applied':
      '数据源缺少已入账的记录，本次同步未生效',
    'Recharge not found': '未找到充值记录',
    'Record changed; retry the request': '记录已变更，请重试',
    'Report billing timezone is unavailable': '报表结算时区不可用',
    'Report source credential environment variable is invalid':
      '报表数据源凭据的环境变量名称无效',
    'Report source credential is not configured': '尚未配置报表数据源凭据',
    'Report source must return an array': '报表数据源必须返回列表',
    'Report source request failed': '请求报表数据源失败',
    'Report source requires HTTPS': '报表数据源必须使用 HTTPS',
    'Report source requires positive channel IDs':
      '报表数据源的渠道 ID 必须为正整数',
    'Report source response is incomplete or too large':
      '报表数据源响应不完整或过大',
    'Report source returned an invalid usage amount':
      '报表数据源返回的用量金额无效',
    'Report source returned an invalid usage count':
      '报表数据源返回的用量计数无效',
    'Report source returned an invalid usage period or model':
      '报表数据源返回的用量时段或模型无效',
    'Report source returned an unsuccessful response':
      '报表数据源返回了失败响应',
    'Report source returned duplicate usage data': '报表数据源返回了重复用量',
    'Report source returned incomplete usage data':
      '报表数据源返回的用量数据不完整',
    'Report source returned invalid usage data': '报表数据源返回的用量数据无效',
    'Report source usage identity is invalid': '报表数据源的用量记录标识无效',
    'Request verification failed. Refresh and try again.':
      '请求验证失败，请刷新后重试。',
    'Same-currency exchange rate must be 1': '相同币种的汇率必须为 1',
    'Select a supplier': '请选择供应商',
    'Select between 1 and 500 channels': '请选择 1 至 500 个渠道',
    'Service unavailable': '服务暂不可用',
    'Source bindings cannot be changed': '不能修改数据源的绑定关系',
    'Source channel belongs to a different supplier head':
      '该数据源渠道归属另一位供应商负责人',
    'Source channels are already assigned or could not be saved':
      '渠道已分配给其他数据源，或保存失败',
    'Source not found': '未找到数据源',
    'Source origin is not allowed by the server': '服务器未允许该数据源地址',
    'Source sync could not be saved': '无法保存数据源同步结果',
    'Source URL must use HTTPS': '数据源地址必须使用 HTTPS',
    'Source vendor mapping is invalid': '数据源的供应商归属信息无效',
    'Sources synced': '数据源已同步',
    'Supplier not found': '未找到供应商',
    'Sync failed': '同步失败',
    'Sync range must be within 91 days and not in the future':
      '同步范围不能超过 91 天，且不能包含未来日期',
    'The last billing manager cannot be disabled.':
      '不能停用最后一个结算管理员。',
    "Today's usage may still change.": '今日用量仍可能变化。',
    'Too many authentication attempts. Try again later.':
      '验证次数过多，请稍后重试。',
    'Unable to create account. Check the username and try again.':
      '无法创建账号，请检查用户名后重试。',
    'Unable to disable account. Refresh and try again.':
      '无法停用账号，请刷新后重试。',
    'Unsynced sources may add expenses to this statement.':
      '未同步的数据源可能为本账单补充费用。',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      '用户名须为 3 至 64 个英文字母、数字、点、下划线、连字符或 @。',
    'Invalid exchange rate mode': '汇率方式无效',
    'Invalid initial balance': '期初金额无效',
    'Initial balance changed; reload and retry': '期初金额已被修改，请刷新后重试',
    'Invalid supplier claim': '供应商账单无效',
    'Invalid usage price': '单价无效',
    'No imported usage for this line': '该产品线当天没有导入用量',
    'Source mapping overlaps another source': '映射与其他数据源重叠',
    'Select a remote profile': '请选择远程站点',
    'Invalid source mapping': '映射无效',
    'Select between 1 and 500 mapped values': '请选择 1 到 500 个映射值',
  },
  'zh-TW': {
    'A reversal reason is required': '請填寫沖正原因',
    'Access denied.': '無權存取。',
    'Account changed. Sign in again.': '帳號已變更，請重新登入。',
    'Amount is out of range': '金額超出允許範圍',
    'Authentication is busy. Try again shortly.': '登入服務忙碌，請稍後重試。',
    'Authentication is unavailable.': '登入服務暫時無法使用。',
    'Awaiting sync': '等待同步',
    'Choose a password that is not commonly used.': '請使用不常見的密碼。',
    'Choose a valid role and supplier.': '請選擇有效的角色與供應商。',
    'Data freshness': '資料更新狀態',
    'Database operation failed': '資料庫操作失敗',
    'Date range must be within 366 days': '日期範圍不能超過 366 天',
    'Enter unique positive channel IDs': '請輸入不重複的正整數渠道 ID',
    'Frontend build is not configured': '尚未設定前端建置目錄',
    'Idempotency key was already used': '此提交識別碼已用於其他內容',
    'In progress': '進行中',
    'Invalid account.': '帳號無效。',
    'Invalid date range': '日期範圍無效',
    'Invalid decimal amount': '金額格式無效',
    'Invalid ledger amount': '帳本金額無效',
    'Invalid recharge': '儲值資訊無效',
    'Invalid report date range': '報表日期範圍無效',
    'Invalid report source request': '報表資料來源請求無效',
    'Invalid report source URL': '報表資料來源網址無效',
    'Invalid request': '請求無效',
    'Invalid request origin': '請求來源無效',
    'Invalid source configuration': '資料來源設定無效',
    'Invalid source URL': '資料來源網址無效',
    'Invalid supplier': '供應商資訊無效',
    'Invalid usage item': '用量項目無效',
    'Invalid username or password.': '使用者名稱或密碼錯誤。',
    'Manual entries only': '僅手動輸入',
    'New password must be different from current password.':
      '新密碼不能與目前密碼相同。',
    'Not found': '找不到',
    'Password must contain between 15 and 128 characters.':
      '密碼長度必須為 15 至 128 個字元。',
    'Pending days: {{count}}': '待同步天數：{{count}}',
    'Permission denied': '權限不足',
    'Please sign in.': '請先登入。',
    'Previously billed source rows are missing; sync was not applied':
      '資料來源缺少已入帳的紀錄，本次同步未套用',
    'Recharge not found': '找不到儲值紀錄',
    'Record changed; retry the request': '紀錄已變更，請重試',
    'Report billing timezone is unavailable': '報表結算時區無法使用',
    'Report source credential environment variable is invalid':
      '報表資料來源憑證的環境變數名稱無效',
    'Report source credential is not configured': '尚未設定報表資料來源憑證',
    'Report source must return an array': '報表資料來源必須傳回陣列',
    'Report source request failed': '請求報表資料來源失敗',
    'Report source requires HTTPS': '報表資料來源必須使用 HTTPS',
    'Report source requires positive channel IDs':
      '報表資料來源的渠道 ID 必須為正整數',
    'Report source response is incomplete or too large':
      '報表資料來源回應不完整或過大',
    'Report source returned an invalid usage amount':
      '報表資料來源傳回的用量金額無效',
    'Report source returned an invalid usage count':
      '報表資料來源傳回的用量計數無效',
    'Report source returned an invalid usage period or model':
      '報表資料來源傳回的用量時段或模型無效',
    'Report source returned an unsuccessful response':
      '報表資料來源傳回失敗回應',
    'Report source returned duplicate usage data': '報表資料來源傳回重複用量',
    'Report source returned incomplete usage data':
      '報表資料來源傳回的用量資料不完整',
    'Report source returned invalid usage data':
      '報表資料來源傳回的用量資料無效',
    'Report source usage identity is invalid':
      '報表資料來源的用量紀錄識別碼無效',
    'Request verification failed. Refresh and try again.':
      '請求驗證失敗，請重新整理後再試。',
    'Same-currency exchange rate must be 1': '相同幣別的匯率必須為 1',
    'Select a supplier': '請選擇供應商',
    'Select between 1 and 500 channels': '請選擇 1 至 500 個渠道',
    'Service unavailable': '服務暫時無法使用',
    'Source bindings cannot be changed': '無法修改資料來源的綁定關係',
    'Source channel belongs to a different supplier head':
      '此資料來源渠道歸屬另一位供應商負責人',
    'Source channels are already assigned or could not be saved':
      '渠道已分配給其他資料來源，或儲存失敗',
    'Source not found': '找不到資料來源',
    'Source origin is not allowed by the server': '伺服器未允許此資料來源網址',
    'Source sync could not be saved': '無法儲存資料來源同步結果',
    'Source URL must use HTTPS': '資料來源網址必須使用 HTTPS',
    'Source vendor mapping is invalid': '資料來源的供應商歸屬資訊無效',
    'Sources synced': '資料來源已同步',
    'Supplier not found': '找不到供應商',
    'Sync failed': '同步失敗',
    'Sync range must be within 91 days and not in the future':
      '同步範圍不能超過 91 天，且不能包含未來日期',
    'The last billing manager cannot be disabled.':
      '不能停用最後一個結算管理員。',
    "Today's usage may still change.": '今日用量仍可能變動。',
    'Too many authentication attempts. Try again later.':
      '驗證次數過多，請稍後重試。',
    'Unable to create account. Check the username and try again.':
      '無法建立帳號，請檢查使用者名稱後重試。',
    'Unable to disable account. Refresh and try again.':
      '無法停用帳號，請重新整理後再試。',
    'Unsynced sources may add expenses to this statement.':
      '未同步的資料來源可能為此帳單補充費用。',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      '使用者名稱須為 3 至 64 個英文字母、數字、點、底線、連字號或 @。',
    'Invalid exchange rate mode': '匯率方式無效',
    'Invalid initial balance': '期初金額無效',
    'Initial balance changed; reload and retry': '期初金額已被修改，請重新整理後重試',
    'Invalid supplier claim': '供應商帳單無效',
    'Invalid usage price': '單價無效',
    'No imported usage for this line': '該產品線當天沒有匯入用量',
    'Source mapping overlaps another source': '映射與其他資料來源重疊',
    'Select a remote profile': '請選擇遠端站點',
    'Invalid source mapping': '映射無效',
    'Select between 1 and 500 mapped values': '請選擇 1 到 500 個映射值',
  },
  fr: {
    'A reversal reason is required': 'Un motif d’annulation est requis',
    'Access denied.': 'Accès refusé.',
    'Account changed. Sign in again.': 'Le compte a changé. Reconnectez-vous.',
    'Amount is out of range': 'Le montant dépasse les limites autorisées',
    'Authentication is busy. Try again shortly.':
      'Authentification occupée. Réessayez sous peu.',
    'Authentication is unavailable.': 'L’authentification est indisponible.',
    'Awaiting sync': 'En attente de synchronisation',
    'Choose a password that is not commonly used.':
      'Choisissez un mot de passe peu courant.',
    'Choose a valid role and supplier.':
      'Choisissez un rôle et un fournisseur valides.',
    'Data freshness': 'Actualisation des données',
    'Database operation failed': 'L’opération en base de données a échoué',
    'Date range must be within 366 days':
      'La période ne doit pas dépasser 366 jours',
    'Enter unique positive channel IDs':
      'Saisissez des ID de canal entiers positifs et uniques',
    'Frontend build is not configured':
      'Le déploiement du frontend n’est pas configuré',
    'Idempotency key was already used':
      'Cette clé d’idempotence a déjà été utilisée',
    'In progress': 'En cours',
    'Invalid account.': 'Compte non valide.',
    'Invalid date range': 'Période non valide',
    'Invalid decimal amount': 'Format de montant non valide',
    'Invalid ledger amount': 'Montant du grand livre non valide',
    'Invalid recharge': 'Informations de versement non valides',
    'Invalid report date range': 'Période du rapport non valide',
    'Invalid report source request':
      'Requête vers la source de rapports non valide',
    'Invalid report source URL': 'URL de la source de rapports non valide',
    'Invalid request': 'Requête non valide',
    'Invalid request origin': 'Origine de la requête non valide',
    'Invalid source configuration': 'Configuration de la source non valide',
    'Invalid source URL': 'URL de la source non valide',
    'Invalid supplier': 'Fournisseur non valide',
    'Invalid usage item': 'Poste de consommation non valide',
    'Invalid username or password.': 'Identifiant ou mot de passe incorrect.',
    'Manual entries only': 'Saisies manuelles uniquement',
    'New password must be different from current password.':
      'Le nouveau mot de passe doit être différent de l’actuel.',
    'Not found': 'Introuvable',
    'Password must contain between 15 and 128 characters.':
      'Le mot de passe doit contenir entre 15 et 128 caractères.',
    'Pending days: {{count}}': 'Jours en attente : {{count}}',
    'Permission denied': 'Autorisation refusée',
    'Please sign in.': 'Veuillez vous connecter.',
    'Previously billed source rows are missing; sync was not applied':
      'Des lignes déjà facturées sont absentes ; synchronisation non appliquée',
    'Recharge not found': 'Versement introuvable',
    'Record changed; retry the request':
      'L’enregistrement a changé ; réessayez',
    'Report billing timezone is unavailable':
      'Fuseau horaire de facturation du rapport indisponible',
    'Report source credential environment variable is invalid':
      'Variable d’environnement des identifiants de la source non valide',
    'Report source credential is not configured':
      'Identifiants de la source de rapports non configurés',
    'Report source must return an array':
      'La source de rapports doit renvoyer un tableau',
    'Report source request failed':
      'Échec de la requête vers la source de rapports',
    'Report source requires HTTPS': 'La source de rapports exige HTTPS',
    'Report source requires positive channel IDs':
      'Les ID de canal de la source doivent être des entiers positifs',
    'Report source response is incomplete or too large':
      'La réponse de la source est incomplète ou trop volumineuse',
    'Report source returned an invalid usage amount':
      'La source a renvoyé un montant de consommation non valide',
    'Report source returned an invalid usage count':
      'La source a renvoyé un nombre d’unités non valide',
    'Report source returned an invalid usage period or model':
      'La source a renvoyé une période ou un modèle non valide',
    'Report source returned an unsuccessful response':
      'La source de rapports a renvoyé une erreur',
    'Report source returned duplicate usage data':
      'La source a renvoyé des données de consommation en double',
    'Report source returned incomplete usage data':
      'La source a renvoyé des données de consommation incomplètes',
    'Report source returned invalid usage data':
      'La source a renvoyé des données de consommation non valides',
    'Report source usage identity is invalid':
      'L’identifiant de consommation de la source est non valide',
    'Request verification failed. Refresh and try again.':
      'La vérification a échoué. Actualisez et réessayez.',
    'Same-currency exchange rate must be 1':
      'Le taux de change doit être 1 pour une même devise',
    'Select a supplier': 'Sélectionnez un fournisseur',
    'Select between 1 and 500 channels': 'Sélectionnez entre 1 et 500 canaux',
    'Service unavailable': 'Service indisponible',
    'Source bindings cannot be changed':
      'Les associations de la source ne peuvent pas être modifiées',
    'Source channel belongs to a different supplier head':
      'Le canal de la source dépend d’un autre responsable de fournisseurs',
    'Source channels are already assigned or could not be saved':
      'Les canaux sont déjà affectés ou n’ont pas pu être enregistrés',
    'Source not found': 'Source introuvable',
    'Source origin is not allowed by the server':
      'L’origine de la source n’est pas autorisée par le serveur',
    'Source sync could not be saved':
      'Impossible d’enregistrer la synchronisation de la source',
    'Source URL must use HTTPS': 'L’URL de la source doit utiliser HTTPS',
    'Source vendor mapping is invalid':
      'L’affectation du fournisseur de la source est invalide',
    'Sources synced': 'Sources synchronisées',
    'Supplier not found': 'Fournisseur introuvable',
    'Sync failed': 'Échec de synchronisation',
    'Sync range must be within 91 days and not in the future':
      'La période de synchronisation ne doit pas dépasser 91 jours ni inclure de dates futures',
    'The last billing manager cannot be disabled.':
      'Le dernier gestionnaire de facturation ne peut pas être désactivé.',
    "Today's usage may still change.":
      'La consommation du jour peut encore changer.',
    'Too many authentication attempts. Try again later.':
      'Trop de tentatives d’authentification. Réessayez plus tard.',
    'Unable to create account. Check the username and try again.':
      'Impossible de créer le compte. Vérifiez l’identifiant et réessayez.',
    'Unable to disable account. Refresh and try again.':
      'Impossible de désactiver le compte. Actualisez et réessayez.',
    'Unsynced sources may add expenses to this statement.':
      'Les sources non synchronisées peuvent ajouter des frais à ce relevé.',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      'L’identifiant doit contenir 3 à 64 lettres, chiffres, points, tirets bas, traits d’union ou @.',
    'Invalid exchange rate mode': 'Mode de taux de change non valide',
    'Invalid initial balance': 'Solde initial non valide',
    'Initial balance changed; reload and retry': 'Le solde initial a changé ; rechargez et réessayez',
    'Invalid supplier claim': 'Relevé du fournisseur non valide',
    'Invalid usage price': 'Prix non valide',
    'No imported usage for this line': 'Aucune consommation importée pour cette ligne',
    'Source mapping overlaps another source': 'La correspondance chevauche une autre source',
    'Select a remote profile': 'Sélectionnez un profil distant',
    'Invalid source mapping': 'Correspondance non valide',
    'Select between 1 and 500 mapped values': 'Sélectionnez entre 1 et 500 valeurs',
  },
  ja: {
    'A reversal reason is required': '取消理由を入力してください',
    'Access denied.': 'アクセスが拒否されました。',
    'Account changed. Sign in again.':
      'アカウントが変更されました。再ログインしてください。',
    'Amount is out of range': '金額が許容範囲外です',
    'Authentication is busy. Try again shortly.':
      '認証処理が混み合っています。しばらくしてから再試行してください。',
    'Authentication is unavailable.': '認証サービスを利用できません。',
    'Awaiting sync': '同期待ち',
    'Choose a password that is not commonly used.':
      'よく使われるパスワードは避けてください。',
    'Choose a valid role and supplier.':
      '有効な役割とサプライヤーを選択してください。',
    'Data freshness': 'データの更新状況',
    'Database operation failed': 'データベース操作に失敗しました',
    'Date range must be within 366 days': '期間は366日以内にしてください',
    'Enter unique positive channel IDs':
      '重複しない正の整数のチャネルIDを入力してください',
    'Frontend build is not configured':
      'フロントエンドのビルドが設定されていません',
    'Idempotency key was already used': 'この冪等キーはすでに使用されています',
    'In progress': '処理中',
    'Invalid account.': '無効なアカウントです。',
    'Invalid date range': '期間が無効です',
    'Invalid decimal amount': '金額の形式が無効です',
    'Invalid ledger amount': '台帳の金額が無効です',
    'Invalid recharge': '入金情報が無効です',
    'Invalid report date range': 'レポートの期間が無効です',
    'Invalid report source request':
      'レポートデータソースへのリクエストが無効です',
    'Invalid report source URL': 'レポートデータソースのURLが無効です',
    'Invalid request': 'リクエストが無効です',
    'Invalid request origin': 'リクエスト元が無効です',
    'Invalid source configuration': 'データソースの設定が無効です',
    'Invalid source URL': 'データソースのURLが無効です',
    'Invalid supplier': 'サプライヤー情報が無効です',
    'Invalid usage item': '使用量項目が無効です',
    'Invalid username or password.':
      'ユーザー名またはパスワードが正しくありません。',
    'Manual entries only': '手動入力のみ',
    'New password must be different from current password.':
      '新しいパスワードは現在のパスワードと異なるものにしてください。',
    'Not found': '見つかりません',
    'Password must contain between 15 and 128 characters.':
      'パスワードは15～128文字で入力してください。',
    'Pending days: {{count}}': '未同期の日数：{{count}}',
    'Permission denied': '権限がありません',
    'Please sign in.': 'ログインしてください。',
    'Previously billed source rows are missing; sync was not applied':
      '請求済みのデータが欠落しているため、同期を適用していません',
    'Recharge not found': '入金記録が見つかりません',
    'Record changed; retry the request':
      '記録が変更されました。再試行してください',
    'Report billing timezone is unavailable':
      'レポートの請求タイムゾーンを利用できません',
    'Report source credential environment variable is invalid':
      'レポートデータソースの認証情報用環境変数が無効です',
    'Report source credential is not configured':
      'レポートデータソースの認証情報が設定されていません',
    'Report source must return an array':
      'レポートデータソースは配列を返す必要があります',
    'Report source request failed':
      'レポートデータソースへのリクエストに失敗しました',
    'Report source requires HTTPS': 'レポートデータソースにはHTTPSが必要です',
    'Report source requires positive channel IDs':
      'レポートデータソースのチャネルIDは正の整数で指定してください',
    'Report source response is incomplete or too large':
      'レポートデータソースの応答が不完全か大きすぎます',
    'Report source returned an invalid usage amount':
      'レポートデータソースが無効な使用金額を返しました',
    'Report source returned an invalid usage count':
      'レポートデータソースが無効な使用回数を返しました',
    'Report source returned an invalid usage period or model':
      'レポートデータソースが無効な使用期間またはモデルを返しました',
    'Report source returned an unsuccessful response':
      'レポートデータソースがエラー応答を返しました',
    'Report source returned duplicate usage data':
      'レポートデータソースが重複した使用量を返しました',
    'Report source returned incomplete usage data':
      'レポートデータソースが不完全な使用量データを返しました',
    'Report source returned invalid usage data':
      'レポートデータソースが無効な使用量データを返しました',
    'Report source usage identity is invalid':
      'レポートデータソースの使用量識別子が無効です',
    'Request verification failed. Refresh and try again.':
      'リクエストの検証に失敗しました。再読み込みして再試行してください。',
    'Same-currency exchange rate must be 1':
      '同一通貨の為替レートは1にしてください',
    'Select a supplier': 'サプライヤーを選択してください',
    'Select between 1 and 500 channels': 'チャネルを1～500件選択してください',
    'Service unavailable': 'サービスを利用できません',
    'Source bindings cannot be changed': 'データソースの紐付けは変更できません',
    'Source channel belongs to a different supplier head':
      'データソースのチャネルは別のサプライヤー責任者に属しています',
    'Source channels are already assigned or could not be saved':
      'チャネルは割り当て済みか、保存できませんでした',
    'Source not found': 'データソースが見つかりません',
    'Source origin is not allowed by the server':
      'サーバーがこのデータソースの接続先を許可していません',
    'Source sync could not be saved':
      'データソースの同期結果を保存できませんでした',
    'Source URL must use HTTPS': 'データソースのURLにはHTTPSを使用してください',
    'Source vendor mapping is invalid':
      'データソースのサプライヤー割り当て情報が無効です',
    'Sources synced': 'データソース同期済み',
    'Supplier not found': 'サプライヤーが見つかりません',
    'Sync failed': '同期に失敗',
    'Sync range must be within 91 days and not in the future':
      '同期期間は91日以内で、未来の日付を含めないでください',
    'The last billing manager cannot be disabled.':
      '最後の請求管理者は無効にできません。',
    "Today's usage may still change.":
      '本日の使用量はまだ変わる可能性があります。',
    'Too many authentication attempts. Try again later.':
      '認証の試行回数が多すぎます。しばらくしてから再試行してください。',
    'Unable to create account. Check the username and try again.':
      'アカウントを作成できません。ユーザー名を確認して再試行してください。',
    'Unable to disable account. Refresh and try again.':
      'アカウントを無効にできません。再読み込みして再試行してください。',
    'Unsynced sources may add expenses to this statement.':
      '未同期のデータソースにより、この明細に費用が追加される可能性があります。',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      'ユーザー名は英字、数字、ドット、アンダースコア、ハイフン、@で3～64文字にしてください。',
    'Invalid exchange rate mode': '為替レート方式が無効です',
    'Invalid initial balance': '初期残高が無効です',
    'Initial balance changed; reload and retry': '初期残高が変更されました。再読み込みしてやり直してください',
    'Invalid supplier claim': '仕入先の請求が無効です',
    'Invalid usage price': '単価が無効です',
    'No imported usage for this line': 'この製品ラインに取込使用量がありません',
    'Source mapping overlaps another source': 'マッピングが他のソースと重複しています',
    'Select a remote profile': 'リモートプロファイルを選択してください',
    'Invalid source mapping': 'マッピングが無効です',
    'Select between 1 and 500 mapped values': '1〜500 個の値を選択してください',
  },
  ru: {
    'A reversal reason is required': 'Укажите причину сторнирования',
    'Access denied.': 'Доступ запрещён.',
    'Account changed. Sign in again.': 'Аккаунт изменён. Войдите снова.',
    'Amount is out of range': 'Сумма вне допустимого диапазона',
    'Authentication is busy. Try again shortly.':
      'Сервис аутентификации занят. Повторите попытку позже.',
    'Authentication is unavailable.': 'Сервис аутентификации недоступен.',
    'Awaiting sync': 'Ожидание синхронизации',
    'Choose a password that is not commonly used.':
      'Выберите пароль, который не входит в число распространённых.',
    'Choose a valid role and supplier.':
      'Выберите допустимую роль и поставщика.',
    'Data freshness': 'Актуальность данных',
    'Database operation failed': 'Ошибка операции с базой данных',
    'Date range must be within 366 days': 'Период не должен превышать 366 дней',
    'Enter unique positive channel IDs':
      'Введите уникальные положительные целочисленные ID каналов',
    'Frontend build is not configured': 'Сборка интерфейса не настроена',
    'Idempotency key was already used': 'Ключ идемпотентности уже использован',
    'In progress': 'В процессе',
    'Invalid account.': 'Недопустимый аккаунт.',
    'Invalid date range': 'Недопустимый период',
    'Invalid decimal amount': 'Недопустимый формат суммы',
    'Invalid ledger amount': 'Недопустимая сумма в реестре',
    'Invalid recharge': 'Недопустимые данные пополнения',
    'Invalid report date range': 'Недопустимый период отчёта',
    'Invalid report source request': 'Недопустимый запрос к источнику отчётов',
    'Invalid report source URL': 'Недопустимый URL источника отчётов',
    'Invalid request': 'Недопустимый запрос',
    'Invalid request origin': 'Недопустимый источник запроса',
    'Invalid source configuration': 'Недопустимая конфигурация источника',
    'Invalid source URL': 'Недопустимый URL источника',
    'Invalid supplier': 'Недопустимые данные поставщика',
    'Invalid usage item': 'Недопустимая запись расхода',
    'Invalid username or password.': 'Неверное имя пользователя или пароль.',
    'Manual entries only': 'Только ручные записи',
    'New password must be different from current password.':
      'Новый пароль должен отличаться от текущего.',
    'Not found': 'Не найдено',
    'Password must contain between 15 and 128 characters.':
      'Пароль должен содержать от 15 до 128 символов.',
    'Pending days: {{count}}': 'Дней в ожидании: {{count}}',
    'Permission denied': 'Недостаточно прав',
    'Please sign in.': 'Войдите в систему.',
    'Previously billed source rows are missing; sync was not applied':
      'Ранее учтённые записи отсутствуют; синхронизация не применена',
    'Recharge not found': 'Пополнение не найдено',
    'Record changed; retry the request': 'Запись изменена; повторите запрос',
    'Report billing timezone is unavailable':
      'Часовой пояс расчёта отчёта недоступен',
    'Report source credential environment variable is invalid':
      'Недопустимая переменная окружения для учётных данных источника отчётов',
    'Report source credential is not configured':
      'Учётные данные источника отчётов не настроены',
    'Report source must return an array':
      'Источник отчётов должен возвращать массив',
    'Report source request failed': 'Ошибка запроса к источнику отчётов',
    'Report source requires HTTPS': 'Источник отчётов требует HTTPS',
    'Report source requires positive channel IDs':
      'ID каналов источника отчётов должны быть положительными целыми числами',
    'Report source response is incomplete or too large':
      'Ответ источника отчётов неполный или слишком большой',
    'Report source returned an invalid usage amount':
      'Источник отчётов вернул недопустимую сумму расхода',
    'Report source returned an invalid usage count':
      'Источник отчётов вернул недопустимое количество',
    'Report source returned an invalid usage period or model':
      'Источник отчётов вернул недопустимый период или модель',
    'Report source returned an unsuccessful response':
      'Источник отчётов вернул ошибку',
    'Report source returned duplicate usage data':
      'Источник отчётов вернул дублирующиеся данные расхода',
    'Report source returned incomplete usage data':
      'Источник отчётов вернул неполные данные расхода',
    'Report source returned invalid usage data':
      'Источник отчётов вернул недопустимые данные расхода',
    'Report source usage identity is invalid':
      'Недопустимый идентификатор расхода в источнике отчётов',
    'Request verification failed. Refresh and try again.':
      'Проверка запроса не пройдена. Обновите страницу и повторите попытку.',
    'Same-currency exchange rate must be 1':
      'Для одинаковых валют курс должен равняться 1',
    'Select a supplier': 'Выберите поставщика',
    'Select between 1 and 500 channels': 'Выберите от 1 до 500 каналов',
    'Service unavailable': 'Сервис недоступен',
    'Source bindings cannot be changed': 'Привязки источника нельзя изменять',
    'Source channel belongs to a different supplier head':
      'Канал источника относится к другому руководителю поставщиков',
    'Source channels are already assigned or could not be saved':
      'Каналы уже назначены или их не удалось сохранить',
    'Source not found': 'Источник не найден',
    'Source origin is not allowed by the server':
      'Сервер не разрешает этот адрес источника',
    'Source sync could not be saved':
      'Не удалось сохранить результат синхронизации источника',
    'Source URL must use HTTPS': 'URL источника должен использовать HTTPS',
    'Source vendor mapping is invalid':
      'Недопустимая привязка поставщика к источнику',
    'Sources synced': 'Источники синхронизированы',
    'Supplier not found': 'Поставщик не найден',
    'Sync failed': 'Ошибка синхронизации',
    'Sync range must be within 91 days and not in the future':
      'Период синхронизации не должен превышать 91 день и включать будущие даты',
    'The last billing manager cannot be disabled.':
      'Нельзя отключить последнего менеджера расчётов.',
    "Today's usage may still change.":
      'Расход за сегодня ещё может измениться.',
    'Too many authentication attempts. Try again later.':
      'Слишком много попыток аутентификации. Повторите позже.',
    'Unable to create account. Check the username and try again.':
      'Не удалось создать аккаунт. Проверьте имя пользователя и повторите попытку.',
    'Unable to disable account. Refresh and try again.':
      'Не удалось отключить аккаунт. Обновите страницу и повторите попытку.',
    'Unsynced sources may add expenses to this statement.':
      'Несинхронизированные источники могут добавить расходы в эту выписку.',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      'Имя пользователя должно содержать 3–64 латинские буквы, цифры, точки, подчёркивания, дефисы или @.',
    'Invalid exchange rate mode': 'Недопустимый режим курса',
    'Invalid initial balance': 'Недопустимый начальный остаток',
    'Initial balance changed; reload and retry': 'Начальный остаток изменён; обновите страницу и повторите',
    'Invalid supplier claim': 'Недопустимые данные поставщика',
    'Invalid usage price': 'Недопустимая цена',
    'No imported usage for this line': 'Для этой линии нет импортированного потребления',
    'Source mapping overlaps another source': 'Сопоставление пересекается с другим источником',
    'Select a remote profile': 'Выберите удалённый профиль',
    'Invalid source mapping': 'Недопустимое сопоставление',
    'Select between 1 and 500 mapped values': 'Выберите от 1 до 500 значений',
  },
  vi: {
    'A reversal reason is required': 'Cần nhập lý do hoàn tác',
    'Access denied.': 'Truy cập bị từ chối.',
    'Account changed. Sign in again.':
      'Tài khoản đã thay đổi. Vui lòng đăng nhập lại.',
    'Amount is out of range': 'Số tiền nằm ngoài phạm vi cho phép',
    'Authentication is busy. Try again shortly.':
      'Dịch vụ xác thực đang bận. Vui lòng thử lại sau.',
    'Authentication is unavailable.': 'Dịch vụ xác thực không khả dụng.',
    'Awaiting sync': 'Đang chờ đồng bộ',
    'Choose a password that is not commonly used.':
      'Vui lòng chọn mật khẩu không phổ biến.',
    'Choose a valid role and supplier.':
      'Vui lòng chọn vai trò và nhà cung cấp hợp lệ.',
    'Data freshness': 'Trạng thái cập nhật dữ liệu',
    'Database operation failed': 'Thao tác cơ sở dữ liệu thất bại',
    'Date range must be within 366 days':
      'Khoảng thời gian không được vượt quá 366 ngày',
    'Enter unique positive channel IDs':
      'Nhập ID kênh là số nguyên dương không trùng lặp',
    'Frontend build is not configured': 'Chưa cấu hình bản dựng giao diện',
    'Idempotency key was already used':
      'Khóa chống trùng lặp này đã được sử dụng',
    'In progress': 'Đang xử lý',
    'Invalid account.': 'Tài khoản không hợp lệ.',
    'Invalid date range': 'Khoảng ngày không hợp lệ',
    'Invalid decimal amount': 'Định dạng số tiền không hợp lệ',
    'Invalid ledger amount': 'Số tiền trong sổ giao dịch không hợp lệ',
    'Invalid recharge': 'Thông tin nạp tiền không hợp lệ',
    'Invalid report date range': 'Khoảng ngày báo cáo không hợp lệ',
    'Invalid report source request': 'Yêu cầu tới nguồn báo cáo không hợp lệ',
    'Invalid report source URL': 'URL nguồn báo cáo không hợp lệ',
    'Invalid request': 'Yêu cầu không hợp lệ',
    'Invalid request origin': 'Nguồn yêu cầu không hợp lệ',
    'Invalid source configuration': 'Cấu hình nguồn không hợp lệ',
    'Invalid source URL': 'URL nguồn không hợp lệ',
    'Invalid supplier': 'Thông tin nhà cung cấp không hợp lệ',
    'Invalid usage item': 'Mục sử dụng không hợp lệ',
    'Invalid username or password.': 'Tên người dùng hoặc mật khẩu không đúng.',
    'Manual entries only': 'Chỉ có mục nhập thủ công',
    'New password must be different from current password.':
      'Mật khẩu mới phải khác mật khẩu hiện tại.',
    'Not found': 'Không tìm thấy',
    'Password must contain between 15 and 128 characters.':
      'Mật khẩu phải có từ 15 đến 128 ký tự.',
    'Pending days: {{count}}': 'Số ngày chờ đồng bộ: {{count}}',
    'Permission denied': 'Không đủ quyền',
    'Please sign in.': 'Vui lòng đăng nhập.',
    'Previously billed source rows are missing; sync was not applied':
      'Thiếu bản ghi nguồn đã hạch toán; chưa áp dụng đồng bộ',
    'Recharge not found': 'Không tìm thấy giao dịch nạp tiền',
    'Record changed; retry the request':
      'Bản ghi đã thay đổi; vui lòng thử lại',
    'Report billing timezone is unavailable':
      'Múi giờ thanh toán báo cáo không khả dụng',
    'Report source credential environment variable is invalid':
      'Biến môi trường chứa thông tin xác thực nguồn báo cáo không hợp lệ',
    'Report source credential is not configured':
      'Chưa cấu hình thông tin xác thực nguồn báo cáo',
    'Report source must return an array': 'Nguồn báo cáo phải trả về một mảng',
    'Report source request failed': 'Yêu cầu tới nguồn báo cáo thất bại',
    'Report source requires HTTPS': 'Nguồn báo cáo yêu cầu HTTPS',
    'Report source requires positive channel IDs':
      'ID kênh của nguồn báo cáo phải là số nguyên dương',
    'Report source response is incomplete or too large':
      'Phản hồi nguồn báo cáo không đầy đủ hoặc quá lớn',
    'Report source returned an invalid usage amount':
      'Nguồn báo cáo trả về số tiền sử dụng không hợp lệ',
    'Report source returned an invalid usage count':
      'Nguồn báo cáo trả về số lượng sử dụng không hợp lệ',
    'Report source returned an invalid usage period or model':
      'Nguồn báo cáo trả về kỳ sử dụng hoặc mô hình không hợp lệ',
    'Report source returned an unsuccessful response':
      'Nguồn báo cáo trả về phản hồi thất bại',
    'Report source returned duplicate usage data':
      'Nguồn báo cáo trả về dữ liệu sử dụng trùng lặp',
    'Report source returned incomplete usage data':
      'Nguồn báo cáo trả về dữ liệu sử dụng không đầy đủ',
    'Report source returned invalid usage data':
      'Nguồn báo cáo trả về dữ liệu sử dụng không hợp lệ',
    'Report source usage identity is invalid':
      'Mã định danh sử dụng của nguồn báo cáo không hợp lệ',
    'Request verification failed. Refresh and try again.':
      'Xác minh yêu cầu thất bại. Hãy tải lại trang và thử lại.',
    'Same-currency exchange rate must be 1':
      'Tỷ giá giữa cùng một loại tiền phải bằng 1',
    'Select a supplier': 'Chọn nhà cung cấp',
    'Select between 1 and 500 channels': 'Chọn từ 1 đến 500 kênh',
    'Service unavailable': 'Dịch vụ không khả dụng',
    'Source bindings cannot be changed': 'Không thể thay đổi liên kết nguồn',
    'Source channel belongs to a different supplier head':
      'Kênh của nguồn thuộc một đầu mối nhà cung cấp khác',
    'Source channels are already assigned or could not be saved':
      'Kênh đã được gán hoặc không thể lưu',
    'Source not found': 'Không tìm thấy nguồn',
    'Source origin is not allowed by the server':
      'Máy chủ không cho phép địa chỉ nguồn này',
    'Source sync could not be saved': 'Không thể lưu kết quả đồng bộ nguồn',
    'Source URL must use HTTPS': 'URL nguồn phải sử dụng HTTPS',
    'Source vendor mapping is invalid':
      'Thông tin liên kết nhà cung cấp của nguồn không hợp lệ',
    'Sources synced': 'Nguồn đã đồng bộ',
    'Supplier not found': 'Không tìm thấy nhà cung cấp',
    'Sync failed': 'Đồng bộ thất bại',
    'Sync range must be within 91 days and not in the future':
      'Khoảng đồng bộ không được vượt quá 91 ngày hoặc bao gồm ngày trong tương lai',
    'The last billing manager cannot be disabled.':
      'Không thể vô hiệu hóa người quản lý thanh toán cuối cùng.',
    "Today's usage may still change.":
      'Mức sử dụng hôm nay vẫn có thể thay đổi.',
    'Too many authentication attempts. Try again later.':
      'Quá nhiều lần thử xác thực. Vui lòng thử lại sau.',
    'Unable to create account. Check the username and try again.':
      'Không thể tạo tài khoản. Hãy kiểm tra tên người dùng và thử lại.',
    'Unable to disable account. Refresh and try again.':
      'Không thể vô hiệu hóa tài khoản. Hãy tải lại trang và thử lại.',
    'Unsynced sources may add expenses to this statement.':
      'Nguồn chưa đồng bộ có thể bổ sung chi phí vào bảng kê này.',
    'Username must contain 3 to 64 letters, numbers, dots, underscores, hyphens or @.':
      'Tên người dùng phải có 3–64 chữ cái Latin, chữ số, dấu chấm, gạch dưới, gạch nối hoặc @.',
    'Invalid exchange rate mode': 'Cách tính tỷ giá không hợp lệ',
    'Invalid initial balance': 'Số dư ban đầu không hợp lệ',
    'Initial balance changed; reload and retry': 'Số dư ban đầu đã thay đổi; hãy tải lại và thử lại',
    'Invalid supplier claim': 'Số liệu nhà cung cấp không hợp lệ',
    'Invalid usage price': 'Đơn giá không hợp lệ',
    'No imported usage for this line': 'Dòng này không có lượng dùng đã nhập',
    'Source mapping overlaps another source': 'Ánh xạ trùng với nguồn khác',
    'Select a remote profile': 'Hãy chọn hồ sơ từ xa',
    'Invalid source mapping': 'Ánh xạ không hợp lệ',
    'Select between 1 and 500 mapped values': 'Hãy chọn từ 1 đến 500 giá trị',
  },
}
