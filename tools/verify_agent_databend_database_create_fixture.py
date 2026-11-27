"""Original Databend database-creation evidence checks used by the schema runner."""
import json

import verify_agent_databend_transport_fixture as transport

REQUIRED = [('./internal/services/databend', name) for name in [
    'TestDatabendDatabaseCreateOriginalAcknowledgementAndGuards',
    'TestDatabendDatabaseCreateLostReplyOccupiedNameAndReplacement',
    'TestDatabendDatabaseCreateCancellationAndCloseWaitForSettlement',
    'TestDatabendDatabaseCreateNativePrivilegeRefusal',
]] + [('./internal/ui/shell', 'TestDatabendNativePublicDatabaseCreatePreviewReceiptAndRecovery')]


def verify_receipt(receipt, state, verified):
    before = receipt['Original']
    target = before['Target']
    namespace = target['Namespace']
    name = namespace['Schema']
    sql = 'CREATE DATABASE "' + name.replace('"', '""') + '"'
    if (receipt.get('State') != state or receipt.get('Verified') is not verified or
            receipt.get('Sequence') != 1 or not receipt.get('DocumentID') or
            namespace['ProfileID'] <= 0 or namespace['Catalog'] or not name or target['Generation'] <= 0 or
            before.get('Engine') != 'databend' or before.get('Exists') is not False or
            before.get('NativeID') or before.get('DDL') or not before.get('Revision') or
            before.get('CreateSQL') != sql or receipt.get('SQL') != sql):
        raise RuntimeError('Database creation lost its original absent target or exact statement')
    native = json.loads(receipt['NativeJSON'])
    response, original = native['Original'], native['Receipt']
    if (original.get('Dispatched') is not True or original.get('ExecutorAcknowledged') is not True or
            response.get('jsonrpc') != '2.0' or not response.get('id')):
        raise RuntimeError('Original database executor acknowledgement missing')
    if state == 'committed':
        result = original.get('Result')
        if (not isinstance(result, dict) or response.get('result') != result or original.get('RemoteError') is not None or
                result.get('columns') != [] or result.get('column_types') != [] or result.get('rows') != [[]] or
                result.get('affected_rows') != 0 or result.get('truncated') is not False):
            raise RuntimeError('Database creation native DDL response was replaced')
    elif state == 'uncertain':
        if (original.get('Result') is not None or (original.get('RemoteError') or {}).get('OperationOutcome') != 'unknown' or
                response.get('error', {}).get('data', {}).get('operationOutcome') != 'unknown'):
            raise RuntimeError('Database creation unknown result was relabelled')
    else:
        raise RuntimeError('Unexpected database creation fixture state')
    actual = receipt.get('Actual')
    if verified:
        if (not isinstance(actual, dict) or actual.get('Target') != target or actual.get('Engine') != 'databend' or
                actual.get('Exists') is not True or not actual.get('Revision') or int(actual.get('NativeID', '0')) <= 0 or
                actual.get('CreateSQL') != sql or actual.get('DDL') != 'CREATE DATABASE `' + name + '` ENGINE=DEFAULT'):
            raise RuntimeError('Database creation metadata observation differs')
    elif actual is not None:
        raise RuntimeError('Unverified original create acquired a later observation')


def verify_database_create(base):
    def read(name):
        return json.loads((base / name).read_text())
    good = read('database-create.json')
    verify_receipt(good['original']['Receipt'], 'committed', True)
    verify_receipt(good['quoted']['Receipt'], 'committed', True)
    if (good['independent_rows'] != [['18446744073709551615']] or
            good['replacement']['NativeID'] == good['original']['Actual']['NativeID'] or
            any(good.get(k) is not True for k in ['sibling_rejected', 'replay_rejected', 'close_released'])):
        raise RuntimeError('Original database identity/guard evidence differs')
    for name, state in [('create_lost61', 'uncertain'), ('create_occupied61', 'uncertain'), ('create_changed61', 'committed')]:
        value = read('database-' + name + '.json')
        verify_receipt(value['original']['Receipt'], state, False)
        if (value.get('dispatches') != 1 or value.get('independent_rows') != [['7']] or
                value.get('read_did_not_settle') is not True or value.get('replay_rejected') is not True):
            raise RuntimeError('Unknown or conflicting original creation was repaired or replayed')
    for name, closing in [('create_cancel61', False), ('create_close61', True)]:
        value = read('database-' + name + '.json')
        verify_receipt(value['original']['Receipt'], 'committed', False)
        if value.get('settlement_waited') is not True or value.get('close') is not closing or value.get('independent_rows') != [[name]]:
            raise RuntimeError('Database cancellation skipped original settlement or claimed rollback')
    denied = read('database-create_denied61.json')
    verify_receipt(denied['original']['Receipt'], 'uncertain', False)
    if (denied.get('native_unprivileged_user_denied') is not True or denied.get('read_did_not_settle') is not True or
            denied.get('independent_rows') is not None):
        raise RuntimeError('Native database privilege refusal was not established')
    message = json.loads(denied['original']['Receipt']['NativeJSON'])['Original']['error']['message'].lower()
    if 'permission' not in message and 'privilege' not in message:
        raise RuntimeError('Native failure was not a privilege refusal')
    lines = (base / 'public-database-create.jsonl').read_text().splitlines()
    if len(lines) != 1:
        raise RuntimeError('Public database creation evidence missing or duplicated')
    public = json.loads(lines[0])
    verify_receipt(public['receipt'], 'committed', True)
    if (public.get('processes') != 2 or public.get('independent_rows') != [['18446744073709551615']] or
            any(public.get(k) is not True for k in ['public_menu', 'preview_cancelled', 'parked_retained', 'namespace_refreshed',
                                                  'unrelated_tab_preserved', 'complete_export', 'recovery_no_replay', 'public_vault_recovery'])):
        raise RuntimeError('Public original create receipt/recovery evidence differs')
    from PIL import Image
    images = {}
    for language in ['zh-Hans', 'en']:
        for width, height in [(1280, 800), (720, 520)]:
            for phase in ['database-create-preview', 'database-create-receipt', 'database-create-disconnected', 'database-create-recovered']:
                path = base / 'workbench' / public['test'] / language / f'{phase}-{width}x{height}.png'
                with Image.open(path) as img:
                    if img.size != (width, height):
                        raise RuntimeError('Database creation capture size differs')
                    img.verify()
                images[str(path.relative_to(base))] = transport.sha(path.read_bytes())
    return {'public': public, 'image_sha256': images, 'native_privilege_refused': True, 'unknown_not_replayed': True}
